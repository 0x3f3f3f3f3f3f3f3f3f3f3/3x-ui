package ssh

import (
	"context"
	"errors"
	"io"
	stdnet "net"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/buf"
	commonctx "github.com/xtls/xray-core/common/ctx"
	"github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
	gossh "golang.org/x/crypto/ssh"
)

type directPayload struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

type channelConn struct {
	gossh.Channel
	transport    *serverTransport
	closeOnce    sync.Once
	slotOnce     sync.Once
	closeErr     error
	requestsDone chan struct{}
	stderrDone   chan struct{}
}

func (c *channelConn) Read(p []byte) (int, error) {
	n, err := c.Channel.Read(p)
	if n > 0 {
		c.transport.activity.Store(time.Now().UnixNano())
	}
	return n, err
}

func (c *channelConn) Write(p []byte) (int, error) {
	n, err := c.Channel.Write(p)
	if n > 0 {
		c.transport.activity.Store(time.Now().UnixNano())
	}
	return n, err
}

func (c *channelConn) Close() error {
	c.closeOnce.Do(func() {
		stop := time.AfterFunc(c.transport.server.limits.channelOpen, c.transport.close)
		defer stop.Stop()
		c.closeErr = c.Channel.Close()
	})
	return c.closeErr
}

func (c *channelConn) startDrains(requests <-chan *gossh.Request) {
	c.requestsDone = make(chan struct{})
	c.stderrDone = make(chan struct{})
	go func() {
		defer close(c.requestsDone)
		for request := range requests {
			stop := time.AfterFunc(c.transport.server.limits.channelOpen, c.transport.close)
			err := request.Reply(false, nil)
			stop.Stop()
			if err != nil && !errors.Is(err, io.EOF) {
				c.transport.close()
			}
		}
	}()
	go func() {
		defer close(c.stderrDone)
		io.Copy(io.Discard, c.Channel.Stderr())
	}()
}

func (c *channelConn) retire() {
	stop := time.AfterFunc(c.transport.server.limits.channelOpen, c.transport.close)
	defer stop.Stop()
	<-c.requestsDone
	<-c.stderrDone
	c.releaseSlot()
}

func (c *channelConn) releaseSlot() {
	c.slotOnce.Do(func() { c.transport.server.releaseChannel(c.transport, c) })
}
func (c *channelConn) LocalAddr() stdnet.Addr  { return c.transport.raw.LocalAddr() }
func (c *channelConn) RemoteAddr() stdnet.Addr { return c.transport.raw.RemoteAddr() }
func (*channelConn) SetDeadline(time.Time) error {
	return errors.New("SSH channel deadlines unsupported")
}

func (*channelConn) SetReadDeadline(time.Time) error {
	return errors.New("SSH channel deadlines unsupported")
}

func (*channelConn) SetWriteDeadline(time.Time) error {
	return errors.New("SSH channel deadlines unsupported")
}

type channelWriter struct{ conn *channelConn }

func (w *channelWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	return (&buf.SequentialWriter{Writer: w.conn}).WriteMultiBuffer(mb)
}
func (w *channelWriter) Close() error { return w.conn.CloseWrite() }
func (w *channelWriter) Interrupt()   { w.conn.Close() }

func inboundTag(ctx context.Context) string {
	if in := session.InboundFromContext(ctx); in != nil {
		return in.Tag
	}
	return ""
}

func (s *Server) reserveChannel(t *serverTransport) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if s.closed || t.closed || t.channelSlots >= s.limits.channels || s.channels >= s.limits.totalChannels {
		return false
	}
	s.channels++
	t.channelSlots++
	return true
}

func (s *Server) releaseChannel(t *serverTransport, c *channelConn) {
	s.mu.Lock()
	t.mu.Lock()
	delete(t.channels, c)
	t.channelSlots--
	t.mu.Unlock()
	s.channels--
	s.mu.Unlock()
}

func (s *Server) directChannel(ctx context.Context, t *serverTransport, incoming gossh.NewChannel, dispatcher routing.Dispatcher) {
	stop := time.AfterFunc(s.limits.channelOpen, t.close)
	defer stop.Stop()
	if incoming.ChannelType() != "direct-tcpip" {
		incoming.Reject(gossh.Prohibited, "only TCP forwarding is permitted")
		return
	}
	var payload directPayload
	if len(incoming.ExtraData()) > 8192 || gossh.Unmarshal(incoming.ExtraData(), &payload) != nil || payload.Host == "" || len(payload.Host) > 253 || payload.Port == 0 || payload.Port > 65535 {
		incoming.Reject(gossh.Prohibited, "invalid TCP forwarding request")
		return
	}
	if !s.reserveChannel(t) {
		incoming.Reject(gossh.ResourceShortage, "channel limit exceeded")
		return
	}
	channel, requests, err := incoming.Accept()
	if err != nil {
		s.releaseChannel(t, nil)
		return
	}
	c := &channelConn{Channel: channel, transport: t}
	c.startDrains(requests)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		c.Close()
		c.retire()
		return
	}
	t.channels[c] = struct{}{}
	t.mu.Unlock()
	go func() {
		defer func() {
			c.Close()
			c.retire()
		}()
		in := session.InboundFromContext(ctx)
		copyInbound := *in
		copyInbound.User = t.user
		copyInbound.Name = "ssh"
		copyInbound.CanSpliceCopy = 3
		copyInbound.Conn = c
		channelCtx := session.ContextWithInbound(t.ctx, &copyInbound)
		channelCtx = commonctx.ContextWithID(channelCtx, session.NewID())
		channelCtx = session.ContextWithOutbounds(channelCtx, []*session.Outbound{{}})
		content := &session.Content{}
		if base := session.ContentFromContext(ctx); base != nil {
			content.SniffingRequest = base.SniffingRequest
		}
		channelCtx = session.ContextWithContent(channelCtx, content)
		dest := net.TCPDestination(net.ParseAddress(payload.Host), net.Port(payload.Port))
		channelCtx = log.ContextWithAccessMessage(channelCtx, &log.AccessMessage{From: copyInbound.Source, To: dest, Status: log.AccessAccepted})
		dispatcher.DispatchLink(channelCtx, dest, &transport.Link{Reader: buf.NewReader(c), Writer: &channelWriter{conn: c}})
	}()
}
