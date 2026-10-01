package snell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	S "github.com/sagernet/sing-snell"
	"github.com/sagernet/sing-snell/snellv5"
	"github.com/sagernet/sing-snell/snellv6"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type echoHandler struct{}

func (echoHandler) NewConnectionEx(_ context.Context, c net.Conn, _, _ M.Socksaddr, close N.CloseHandlerFunc) {
	p, err := io.ReadAll(c)
	if err == nil {
		_, err = c.Write(p)
	}
	if half, ok := c.(interface{ CloseWrite() error }); ok {
		if e := half.CloseWrite(); err == nil {
			err = e
		}
	}
	if close != nil {
		close(err)
	}
}

type timeoutPolicy struct{}

func (timeoutPolicy) Type() any    { return policy.ManagerType() }
func (timeoutPolicy) Start() error { return nil }
func (timeoutPolicy) Close() error { return nil }
func (timeoutPolicy) ForLevel(uint32) policy.Session {
	return policy.Session{Timeouts: policy.Timeout{Handshake: 100 * time.Millisecond, ConnectionIdle: 100 * time.Millisecond, UplinkOnly: 100 * time.Millisecond, DownlinkOnly: 100 * time.Millisecond}}
}
func (timeoutPolicy) ForSystem() policy.System { return policy.System{} }

type idleReader struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (r *idleReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	<-r.ctx.Done()
	return nil, r.ctx.Err()
}
func (r *idleReader) Interrupt() { r.cancel() }

func TestOutboundUDPIdleCleanupUsesPolicy(t *testing.T) {
	server, _ := sourceServer(t, 6)
	out, err := NewClient(context.Background(), &ClientConfig{Version: 6, Psk: "native-snell-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: uint32(server.Port)})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	instance := new(core.Instance)
	if err := instance.AddFeature(timeoutPolicy{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.UDPDestination(X.LocalHostIP, 53)}})
	done := make(chan error, 1)
	go func() {
		done <- out.Process(ctx, &transport.Link{Reader: &idleReader{ctx: ctx, cancel: cancel}, Writer: new(bytesWriter)}, new(countDialer))
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("idle session unexpectedly succeeded")
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("native UDP ignored its idle policy or leaked a copy goroutine")
	}
	out.mu.Lock()
	active := len(out.connections)
	out.mu.Unlock()
	if active != 0 {
		t.Fatalf("idle UDP left physical connections: %d", active)
	}
}

type lateDialer struct {
	entered chan struct{}
	release chan struct{}
	conn    net.Conn
}

func (d *lateDialer) Dial(context.Context, X.Destination) (stat.Connection, error) {
	close(d.entered)
	<-d.release
	return d.conn, nil
}
func (*lateDialer) DestIpAddress() X.IP                                   { return nil }
func (*lateDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func TestOutboundCloseFencesLateDials(t *testing.T) {
	for _, version := range []uint32{4, 5, 6} {
		for _, network := range []X.Network{X.Network_TCP, X.Network_UDP} {
			t.Run(fmt.Sprintf("v%d/%s", version, network), func(t *testing.T) {
				out, err := NewClient(context.Background(), &ClientConfig{Version: version, Psk: "native-snell-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 1})
				if err != nil {
					t.Fatal(err)
				}
				c, peer := net.Pipe()
				defer peer.Close()
				d := &lateDialer{entered: make(chan struct{}), release: make(chan struct{}), conn: c}
				ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: X.Destination{Network: network, Address: X.LocalHostIP, Port: 80}}})
				done := make(chan error, 1)
				go func() {
					done <- out.Process(ctx, &transport.Link{Reader: &eofReader{payload: []byte("payload")}, Writer: new(bytesWriter)}, d)
				}()
				<-d.entered
				if err := out.Close(); err != nil {
					t.Fatal(err)
				}
				close(d.release)
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("late dial entered the closed outbound")
					}
				case <-time.After(time.Second):
					t.Fatal("late dial was not fenced")
				}
				peer.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
					t.Fatalf("late raw connection stayed open: %v", err)
				}
				out.mu.Lock()
				active := len(out.connections)
				out.mu.Unlock()
				if active != 0 {
					t.Fatal("late dial remained in physical registry")
				}
			})
		}
	}
}

type capacityDialer struct {
	entered chan struct{}
	release chan struct{}
}

func (d *capacityDialer) Dial(context.Context, X.Destination) (stat.Connection, error) {
	d.entered <- struct{}{}
	<-d.release
	c, peer := net.Pipe()
	peer.Close()
	return c, nil
}
func (*capacityDialer) DestIpAddress() X.IP                                   { return nil }
func (*capacityDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func TestOutboundCapacityIncludesPendingDials(t *testing.T) {
	out, err := NewClient(context.Background(), &ClientConfig{Version: 6, Psk: "native-snell-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	d := &capacityDialer{entered: make(chan struct{}, 129), release: make(chan struct{})}
	done := make(chan error, 129)
	for range 128 {
		go func() {
			ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: X.UDPDestination(X.LocalHostIP, 53)}})
			done <- out.Process(ctx, &transport.Link{Reader: &eofReader{payload: []byte("packet")}, Writer: new(bytesWriter)}, d)
		}()
	}
	for range 128 {
		select {
		case <-d.entered:
		case <-time.After(time.Second):
			out.Close()
			close(d.release)
			t.Fatal("pending capacity reservations did not admit available slots")
		}
	}
	go func() {
		ownCtx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: X.UDPDestination(X.LocalHostIP, 53)}})
		done <- out.Process(ownCtx, &transport.Link{Reader: &eofReader{payload: []byte("packet")}, Writer: new(bytesWriter)}, d)
	}()
	enteredExtra := false
	completed := 0
	select {
	case <-d.entered:
		enteredExtra = true
	case err := <-done:
		completed++
		if err == nil {
			t.Error("capacity overflow succeeded")
		}
	case <-time.After(time.Second):
		t.Error("overflow admission was not bounded")
	}
	out.Close()
	close(d.release)
	for completed < 129 {
		select {
		case <-done:
			completed++
		case <-time.After(time.Second):
			t.Fatal("pending dials did not leave the closed handler")
		}
	}
	if enteredExtra {
		t.Fatal("pending dials were omitted from the physical capacity bound")
	}
}

func (echoHandler) NewPacketConnectionEx(context.Context, N.PacketConn, M.Socksaddr, M.Socksaddr, N.CloseHandlerFunc) {
	panic("unused packet handler")
}

type countDialer struct {
	count   atomic.Int32
	gateway X.Address
}

func (d *countDialer) Dial(ctx context.Context, dest X.Destination) (stat.Connection, error) {
	d.count.Add(1)
	return (&net.Dialer{}).DialContext(ctx, "tcp", dest.NetAddr())
}
func (d *countDialer) DestIpAddress() X.IP { return nil }
func (d *countDialer) SetOutboundGateway(_ context.Context, o *session.Outbound) {
	if d.gateway != nil {
		o.Gateway = d.gateway
	}
}

type eofReader struct {
	payload []byte
	done    bool
}

func (r *eofReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r.done {
		return nil, io.EOF
	}
	r.done = true
	return buf.MultiBuffer{buf.FromBytes(r.payload)}, io.EOF
}
func (*eofReader) Interrupt() {}

type bytesWriter struct{ bytes.Buffer }

func (w *bytesWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	defer buf.ReleaseMulti(mb)
	for _, b := range mb {
		w.Write(b.Bytes())
	}
	return nil
}
func (*bytesWriter) Interrupt() {}

func sourceServer(t *testing.T, version uint32) (*net.TCPAddr, S.Service) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	var service S.Service
	if version == 6 {
		service, err = snellv6.NewService(snellv6.ServerOptions{PSK: []byte("native-snell-test-secret"), Handler: echoHandler{}})
	} else {
		service, err = snellv5.NewService(snellv5.ServiceOptions{PSK: []byte("native-snell-test-secret"), Handler: echoHandler{}})
	}
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				_ = service.NewConnection(context.Background(), c, M.SocksaddrFromNet(c.RemoteAddr()), nil)
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr), service
}

func TestOutboundReuseScopeAndCredentialFence(t *testing.T) {
	for _, version := range []uint32{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			server, _ := sourceServer(t, version)
			out, err := NewClient(context.Background(), &ClientConfig{Version: version, Psk: "native-snell-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: uint32(server.Port), Reuse: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { out.Close() })
			u := &protocol.MemoryUser{ClientID: "owner", Email: "first"}
			d := new(countDialer)
			run := func(user *protocol.MemoryUser, tag string, d *countDialer, mark int32) {
				t.Helper()
				ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: user, Tag: tag})
				ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.TCPDestination(X.LocalHostIP, 80)}})
				ctx = session.ContextWithSockopt(ctx, &session.Sockopt{Mark: mark})
				writer := new(bytesWriter)
				if err := out.Process(ctx, &transport.Link{Reader: &eofReader{payload: []byte("payload")}, Writer: writer}, d); err != nil {
					t.Fatal(err)
				}
				if writer.String() != "payload" {
					t.Fatal("decoded reusable payload changed")
				}
			}
			run(u, "a", d, 0)
			run(u, "a", d, 0)
			if d.count.Load() != 1 {
				t.Fatalf("same immutable scope failed to reuse: dials=%d", d.count.Load())
			}
			u2 := &protocol.MemoryUser{ClientID: "owner", Email: "first"}
			run(u2, "a", d, 0)
			if d.count.Load() != 2 {
				t.Fatalf("different credential generation reused previous physical transport: %d", d.count.Load())
			}
			run(u2, "b", d, 0)
			run(u2, "b", d, 1)
			d.gateway = X.IPAddress([]byte{127, 0, 0, 2})
			run(u2, "b", d, 1)
			if d.count.Load() != 5 {
				t.Fatalf("tag/mark/resolved gateway shared a transport: %d", d.count.Load())
			}
			d2 := new(countDialer)
			run(u2, "b", d2, 1)
			if d2.count.Load() != 1 {
				t.Fatal("different injected dialer shared a physical transport")
			}
			u.RevokeCredential()
			before := d.count.Load()
			ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: u, Tag: "a"})
			ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.TCPDestination(X.LocalHostIP, 80)}})
			if err := out.Process(ctx, &transport.Link{Reader: &eofReader{payload: []byte("denied")}, Writer: new(bytesWriter)}, d); err == nil || d.count.Load() != before {
				t.Fatalf("revoked credential reused or redialed physical transport: error=%v dials=%d", err, d.count.Load())
			}
		})
	}
}

func TestOutboundNonreuseHandshakeWriteUsesPolicy(t *testing.T) {
	for _, version := range []uint32{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			out, err := NewClient(context.Background(), &ClientConfig{Version: version, Psk: "native-snell-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			instance := new(core.Instance)
			if err = instance.AddFeature(timeoutPolicy{}); err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.TCPDestination(X.LocalHostIP, 80)}})
			c, peer := net.Pipe()
			defer peer.Close()
			release := make(chan struct{})
			close(release)
			d := &lateDialer{entered: make(chan struct{}), release: release, conn: c}
			done := make(chan error, 1)
			go func() {
				done <- out.Process(ctx, &transport.Link{Reader: &eofReader{payload: []byte("payload")}, Writer: new(bytesWriter)}, d)
			}()
			<-d.entered
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stalled handshake succeeded")
				}
			case <-time.After(500 * time.Millisecond):
				cancel()
				out.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("could not terminate review probe")
				}
				t.Fatal("non-reuse initial Snell write remained blocked beyond 100ms handshake and idle policies")
			}
		})
	}
}
