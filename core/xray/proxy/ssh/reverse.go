package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common/buf"
	gossh "golang.org/x/crypto/ssh"
)

type reversePolicy struct {
	enabled, portZero bool
	binds             map[string]struct{}
	sources           []netip.Prefix
	portFrom, portTo  uint32
	listeners         int
}

type (
	forwardPayload struct {
		Address string
		Port    uint32
	}
	forwardedPayload struct {
		Address    string
		Port       uint32
		Origin     string
		OriginPort uint32
	}
)

type reverseListener struct {
	transport *serverTransport
	listener  net.Listener
	address   string
	port      uint32
	mu        sync.Mutex
	closed    bool
	streams   map[*reverseStream]struct{}
}

type reverseStream struct {
	peer    net.Conn
	mu      sync.Mutex
	closed  bool
	channel *channelConn
}

func buildReversePolicy(c *ReverseConfig) (reversePolicy, error) {
	p := reversePolicy{}
	if c == nil || !c.Enabled {
		return p, nil
	}
	if len(c.BindAddresses) == 0 || len(c.BindAddresses) > 16 || len(c.SourceCidrs) == 0 || len(c.SourceCidrs) > 64 || c.PortFrom == 0 || c.PortTo < c.PortFrom || c.PortTo > 65535 {
		return p, ErrConfiguration
	}
	p.enabled = true
	p.portZero = c.AllowPortZero
	p.portFrom = c.PortFrom
	p.portTo = c.PortTo
	p.binds = make(map[string]struct{})
	var err error
	p.listeners, err = bounded(c.MaxListeners, 4, 16)
	if err != nil {
		return p, err
	}
	for _, address := range c.BindAddresses {
		ip, err := netip.ParseAddr(address)
		if err != nil || ip.Zone() != "" {
			return p, ErrConfiguration
		}
		p.binds[ip.Unmap().String()] = struct{}{}
	}
	for _, source := range c.SourceCidrs {
		prefix, err := netip.ParsePrefix(source)
		if err != nil || prefix != prefix.Masked() {
			return p, ErrConfiguration
		}
		p.sources = append(p.sources, prefix)
	}
	return p, nil
}

func (p reversePolicy) bindAddress(request string) (string, bool) {
	if request == "localhost" {
		for _, address := range []string{"127.0.0.1", "::1"} {
			if _, ok := p.binds[address]; ok {
				return address, true
			}
		}
		return "", false
	}
	address, err := netip.ParseAddr(request)
	if err != nil || address.Zone() != "" {
		return "", false
	}
	address = address.Unmap()
	_, ok := p.binds[address.String()]
	return address.String(), ok
}

func (p reversePolicy) sourceAllowed(address net.Addr) bool {
	a, ok := address.(*net.TCPAddr)
	if !ok {
		return false
	}
	ip, ok := netip.AddrFromSlice(a.IP)
	if !ok {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range p.sources {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func forwardKey(address string, port uint32) string {
	return net.JoinHostPort(address, strconv.FormatUint(uint64(port), 10))
}

func (t *serverTransport) globalRequest(ctx context.Context, r *gossh.Request) {
	stop := time.AfterFunc(t.server.limits.channelOpen, t.close)
	defer stop.Stop()
	if r.Type == "keepalive@openssh.com" {
		r.Reply(true, nil)
		return
	}
	var payload forwardPayload
	policy := t.server.reverse
	if !policy.enabled || (r.Type != "tcpip-forward" && r.Type != "cancel-tcpip-forward") || len(r.Payload) > 1024 || gossh.Unmarshal(r.Payload, &payload) != nil || payload.Port > 65535 {
		r.Reply(false, nil)
		return
	}
	key := forwardKey(payload.Address, payload.Port)
	if r.Type == "cancel-tcpip-forward" {
		t.mu.Lock()
		listener := t.forwards[key]
		delete(t.forwards, key)
		t.mu.Unlock()
		if listener != nil {
			listener.close()
		}
		r.Reply(listener != nil, nil)
		return
	}
	address, allowed := policy.bindAddress(payload.Address)
	if !allowed || payload.Port == 0 && !policy.portZero || payload.Port != 0 && (payload.Port < policy.portFrom || payload.Port > policy.portTo) {
		r.Reply(false, nil)
		return
	}
	t.mu.Lock()
	if t.closed || len(t.forwards) >= policy.listeners || t.forwards[key] != nil {
		t.mu.Unlock()
		r.Reply(false, nil)
		return
	}
	listener, err := (&net.ListenConfig{}).Listen(t.ctx, "tcp", forwardKey(address, payload.Port))
	if err != nil {
		t.mu.Unlock()
		r.Reply(false, nil)
		return
	}
	actualPort := uint32(listener.Addr().(*net.TCPAddr).Port)
	if actualPort < policy.portFrom || actualPort > policy.portTo {
		listener.Close()
		t.mu.Unlock()
		r.Reply(false, nil)
		return
	}
	key = forwardKey(payload.Address, actualPort)
	forward := &reverseListener{transport: t, listener: listener, address: payload.Address, port: actualPort, streams: make(map[*reverseStream]struct{})}
	t.forwards[key] = forward
	t.mu.Unlock()
	var reply []byte
	if payload.Port == 0 {
		reply = gossh.Marshal(struct{ Port uint32 }{actualPort})
	}
	if err := r.Reply(true, reply); err != nil {
		t.mu.Lock()
		delete(t.forwards, key)
		t.mu.Unlock()
		forward.close()
		return
	}
	t.activity.Store(time.Now().UnixNano())
	go forward.accept(ctx)
}

func (r *reverseListener) close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	streams := make([]*reverseStream, 0, len(r.streams))
	for s := range r.streams {
		streams = append(streams, s)
	}
	r.mu.Unlock()
	r.listener.Close()
	for _, s := range streams {
		s.close()
	}
}

func (s *reverseStream) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	channel := s.channel
	s.mu.Unlock()
	s.peer.Close()
	if channel != nil {
		channel.Close()
	}
}

func (r *reverseListener) accept(ctx context.Context) {
	for {
		peer, err := r.listener.Accept()
		if err != nil {
			return
		}
		if !r.transport.server.reverse.sourceAllowed(peer.RemoteAddr()) {
			peer.Close()
			continue
		}
		r.mu.Lock()
		if r.closed || !r.transport.server.reserveChannel(r.transport) {
			r.mu.Unlock()
			peer.Close()
			continue
		}
		stream := &reverseStream{peer: peer}
		r.streams[stream] = struct{}{}
		r.mu.Unlock()
		go r.process(ctx, stream)
	}
}

func (r *reverseListener) process(ctx context.Context, stream *reverseStream) {
	t := r.transport
	var channel *channelConn
	defer func() {
		if channel != nil {
			channel.Close()
		}
		stream.close()
		r.mu.Lock()
		delete(r.streams, stream)
		r.mu.Unlock()
		if channel == nil {
			t.server.releaseChannel(t, nil)
		} else {
			channel.retire()
		}
	}()
	lease, err := t.server.clients.Open(t.ctx, clientpolicy.Metadata{ClientID: t.user.ClientID, InboundTag: inboundTag(ctx), AuthenticatedAccount: t.user.Email, OriginalTarget: "ssh-reverse-listener:" + r.listener.Addr().String(), ActualTarget: "unknown-client-target"}, stream.close)
	if err != nil {
		return
	}
	defer lease.Release()
	untrack, err := t.user.TrackSession(lease.Close)
	if err != nil {
		return
	}
	defer untrack()
	origin := stream.peer.RemoteAddr().(*net.TCPAddr)
	ch, requests, err := t.openReverseChannel(gossh.Marshal(forwardedPayload{r.address, r.port, origin.IP.String(), uint32(origin.Port)}))
	if err != nil {
		return
	}
	channel = &channelConn{Channel: ch, transport: t}
	channel.startDrains(requests)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.channels[channel] = struct{}{}
	t.mu.Unlock()
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		return
	}
	stream.channel = channel
	stream.mu.Unlock()
	duplex(t.ctx, func() error {
		err := buf.Copy(buf.NewReader(channel), &reverseWriter{writer: stream.peer, session: lease, direction: clientpolicy.Upload})
		if tcp, ok := stream.peer.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		return err
	}, func() error {
		err := buf.Copy(buf.NewReader(stream.peer), &reverseWriter{writer: channel, session: lease, direction: clientpolicy.Download})
		channel.CloseWrite()
		return err
	}, stream.close)
}

type channelOpenResult struct {
	channel  gossh.Channel
	requests <-chan *gossh.Request
	err      error
}

func (t *serverTransport) openReverseChannel(payload []byte) (gossh.Channel, <-chan *gossh.Request, error) {
	result := make(chan channelOpenResult, 1)
	go func() {
		c, r, e := t.conn.OpenChannel("forwarded-tcpip", payload)
		result <- channelOpenResult{c, r, e}
	}()
	timer := time.NewTimer(t.server.limits.channelOpen)
	defer timer.Stop()
	select {
	case out := <-result:
		return out.channel, out.requests, out.err
	case <-timer.C:
		t.close()
	case <-t.ctx.Done():
		t.close()
	}
	out := <-result
	if out.channel != nil {
		out.channel.Close()
	}
	return nil, nil, context.DeadlineExceeded
}

type reverseWriter struct {
	writer    io.Writer
	session   *clientpolicy.Session
	direction clientpolicy.Direction
}

func (w *reverseWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	for len(mb) > 0 {
		limit, err := w.session.StreamChunkSize(w.direction)
		if err != nil {
			buf.ReleaseMulti(mb)
			return err
		}
		var part buf.MultiBuffer
		mb, part = buf.SplitSize(mb, int32(limit))
		finish, err := w.session.AdmitPayload(w.direction, uint64(part.Len()))
		if err != nil {
			if errors.Is(err, clientpolicy.ErrPacketTooLarge) {
				mb = append(part, mb...)
				continue
			}
			buf.ReleaseMulti(part)
			buf.ReleaseMulti(mb)
			return err
		}
		for _, b := range part {
			if err = buf.WriteAllBytes(w.writer, b.Bytes(), nil); err != nil {
				break
			}
		}
		buf.ReleaseMulti(part)
		finish()
		if err != nil {
			buf.ReleaseMulti(mb)
			return err
		}
	}
	return nil
}

func duplex(ctx context.Context, first, second func() error, closeFn func()) error {
	done := make(chan error, 2)
	go func() { done <- first() }()
	go func() { done <- second() }()
	var result error
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				result = errors.Join(result, err)
				closeFn()
			}
		case <-ctx.Done():
			closeFn()
			result = errors.Join(result, ctx.Err())
			result = errors.Join(result, <-done)
		}
	}
	return result
}
