package ssh

import (
	"context"
	"io"
	stdnet "net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
	gossh "golang.org/x/crypto/ssh"
)

type delayedCloseChannel struct {
	data         *io.PipeReader
	dataPeer     *io.PipeWriter
	extended     *io.PipeReader
	extendedPeer *io.PipeWriter
	requests     chan *gossh.Request
	closeSent    chan struct{}
	closeOnce    sync.Once
	ackOnce      sync.Once
}

func delayedChannel() *delayedCloseChannel {
	r, w := io.Pipe()
	er, ew := io.Pipe()
	return &delayedCloseChannel{data: r, dataPeer: w, extended: er, extendedPeer: ew, requests: make(chan *gossh.Request), closeSent: make(chan struct{})}
}
func (c *delayedCloseChannel) Read(p []byte) (int, error) { return c.data.Read(p) }
func (*delayedCloseChannel) Write(p []byte) (int, error)  { return len(p), nil }
func (c *delayedCloseChannel) Close() error {
	c.closeOnce.Do(func() { close(c.closeSent) })
	return nil
}
func (*delayedCloseChannel) CloseWrite() error                              { return nil }
func (*delayedCloseChannel) SendRequest(string, bool, []byte) (bool, error) { return false, nil }
func (c *delayedCloseChannel) Stderr() io.ReadWriter {
	return struct {
		io.Reader
		io.Writer
	}{c.extended, io.Discard}
}

func (c *delayedCloseChannel) ack() {
	c.ackOnce.Do(func() { close(c.requests); c.dataPeer.Close(); c.extendedPeer.Close() })
}

type delayedNewChannel struct {
	channel  *delayedCloseChannel
	port     uint32
	rejected bool
}

func (c *delayedNewChannel) Accept() (gossh.Channel, <-chan *gossh.Request, error) {
	return c.channel, c.channel.requests, nil
}

func (c *delayedNewChannel) Reject(gossh.RejectionReason, string) error {
	c.rejected = true
	return nil
}
func (*delayedNewChannel) ChannelType() string { return "direct-tcpip" }
func (c *delayedNewChannel) ExtraData() []byte {
	return gossh.Marshal(directPayload{Host: "127.0.0.1", Port: c.port})
}

type delayedRetirementConn struct {
	stdnet.Conn
	channels []*delayedCloseChannel
}

func (c delayedRetirementConn) Close() error {
	for _, ch := range c.channels {
		ch.ack()
	}
	return c.Conn.Close()
}

type delayedDispatcher struct{}

func (delayedDispatcher) Type() interface{} { return routing.DispatcherType() }
func (delayedDispatcher) Start() error      { return nil }
func (delayedDispatcher) Close() error      { return nil }
func (delayedDispatcher) Dispatch(context.Context, net.Destination) (*transport.Link, error) {
	return nil, nil
}

func (delayedDispatcher) DispatchLink(_ context.Context, dest net.Destination, link *transport.Link) error {
	if dest.Port == 81 {
		return buf.Copy(link.Reader, buf.Discard)
	}
	return nil
}

func TestChannelCapacityRetainsDelayedPeerCloseAndOwnedDrains(t *testing.T) {
	limits, err := serverLimits(&ServerConfig{MaxChannelsPerConnection: 2, MaxChannels: 2, ChannelOpenTimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{limits: limits, connections: make(map[*serverTransport]struct{}), perClient: map[string]int{"owner": 1}}
	first, healthy, third := delayedChannel(), delayedChannel(), delayedChannel()
	raw, peer := stdnet.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	tr := &serverTransport{server: s, raw: delayedRetirementConn{Conn: raw, channels: []*delayedCloseChannel{first, healthy, third}}, ctx: ctx, cancel: cancel, user: &protocol.MemoryUser{ClientID: "owner"}, channels: make(map[*channelConn]struct{}), forwards: make(map[string]*reverseListener)}
	s.connections[tr] = struct{}{}
	defer tr.close()
	ctx = session.ContextWithInbound(ctx, &session.Inbound{Tag: "controlled-close"})
	s.directChannel(ctx, tr, &delayedNewChannel{channel: first, port: 80}, delayedDispatcher{})
	s.directChannel(ctx, tr, &delayedNewChannel{channel: healthy, port: 81}, delayedDispatcher{})
	select {
	case <-first.closeSent:
	case <-time.After(time.Second):
		t.Fatal("completed channel did not send CLOSE")
	}
	s.mu.Lock()
	owned := s.channels
	s.mu.Unlock()
	if owned != 2 {
		t.Fatalf("locally closed channel released capacity before peer CLOSE and drain retirement: owned=%d want2", owned)
	}
	blocked := &delayedNewChannel{channel: third, port: 82}
	s.directChannel(ctx, tr, blocked, delayedDispatcher{})
	if !blocked.rejected {
		t.Fatal("delayed peer CLOSE escaped maximum channel bound")
	}
	first.ack()
	waitCount := func(want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			n := s.channels
			s.mu.Unlock()
			if n == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("owned channel drains did not retire to %d", want)
	}
	waitCount(1)
	select {
	case <-healthy.closeSent:
		t.Fatal("normal sibling completion closed healthy channel")
	default:
	}
	retry := &delayedNewChannel{channel: third, port: 82}
	s.directChannel(ctx, tr, retry, delayedDispatcher{})
	if retry.rejected {
		t.Fatal("retired channel did not restore capacity")
	}
	select {
	case <-third.closeSent:
	case <-time.After(time.Second):
		t.Fatal("retry channel did not finish")
	}
	tr.close()
	waitCount(0)
}

func TestChannelRetirementTimeoutClosesTransportAndJoinsOwnedDrains(t *testing.T) {
	limits, err := serverLimits(&ServerConfig{MaxChannelsPerConnection: 2, MaxChannels: 2, ChannelOpenTimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{limits: limits, connections: make(map[*serverTransport]struct{}), perClient: map[string]int{"owner": 1}}
	closing, healthy := delayedChannel(), delayedChannel()
	raw, peer := stdnet.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	tr := &serverTransport{server: server, raw: delayedRetirementConn{Conn: raw, channels: []*delayedCloseChannel{closing, healthy}}, ctx: ctx, cancel: cancel, user: &protocol.MemoryUser{ClientID: "owner"}, channels: make(map[*channelConn]struct{}), forwards: make(map[string]*reverseListener)}
	server.connections[tr] = struct{}{}
	defer tr.close()
	channelCtx := session.ContextWithInbound(ctx, &session.Inbound{Tag: "controlled-missing-ack"})
	server.directChannel(channelCtx, tr, &delayedNewChannel{channel: closing, port: 80}, delayedDispatcher{})
	server.directChannel(channelCtx, tr, &delayedNewChannel{channel: healthy, port: 81}, delayedDispatcher{})
	select {
	case <-closing.closeSent:
	case <-time.After(time.Second):
		t.Fatal("completed channel did not send local CLOSE")
	}
	server.mu.Lock()
	owned := server.channels
	server.mu.Unlock()
	if owned != 2 {
		t.Fatalf("missing acknowledgment prematurely released owned capacity: %d", owned)
	}
	begin := time.Now()
	select {
	case <-ctx.Done():
	case <-time.After(1800 * time.Millisecond):
		t.Fatal("missing peer CLOSE acknowledgment retained transport beyond channel bound")
	}
	if elapsed := time.Since(begin); elapsed < 850*time.Millisecond {
		t.Fatalf("missing acknowledgment closed transport prematurely: %s", elapsed)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		owned = server.channels
		server.mu.Unlock()
		if owned == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed-out transport did not join and release owned channel drains: %d", owned)
}
