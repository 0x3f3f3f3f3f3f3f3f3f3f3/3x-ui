package mieru_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	miServer "github.com/enfein/mieru/v3/apis/server"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/mieru"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/pipe"
	"google.golang.org/protobuf/proto"
)

func TestReviewCancellationInterruptsPendingOfficialHandshake(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) { reviewPendingHandshake(t, mode, true) })
	}
}

func TestNativeMieruOutboundHandshakeTimeoutIsAbsolute(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) { reviewPendingHandshake(t, mode, false) })
	}
}

func reviewPendingHandshake(t *testing.T, mode string, cancelAfterAuth bool) {
	port := reservePort(t, mode)
	protocol := appctlpb.TransportProtocol_TCP
	if mode == "UDP" {
		protocol = appctlpb.TransportProtocol_UDP
	}
	server := miServer.NewServer()
	if err := server.Store(&miServer.ServerConfig{Config: &appctlpb.ServerConfig{
		ListenIPAddress: proto.String("127.0.0.1"), PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: protocol.Enum()}},
		Users: []*appctlpb.User{{Name: proto.String("alice"), Password: proto.String("native-business-secret")}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	releasePeer := make(chan struct{})
	defer close(releasePeer)
	accepted := make(chan struct{})
	go func() {
		conn, _, err := server.Accept()
		if err == nil {
			defer conn.Close()
			if _, err := conn.Write([]byte{5}); err != nil {
				return
			}
			close(accepted)
			<-releasePeer
		}
	}()
	clientCtx := context.Background()
	if !cancelAfterAuth {
		instance := nativeCoreConfigured(t, reservePort(t, mode), mode, func(config map[string]any) {
			config["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{"handshake": 1}}}
		})
		clientCtx = context.WithValue(clientCtx, core.XrayKey(1), instance)
	}
	client, err := mieru.NewClient(clientCtx, &mieru.ClientConfig{Address: xnet.NewIPOrDomain(xnet.LocalHostIP), Port: uint32(port), Username: "alice", Password: "native-business-secret", Transport: mode, Multiplexing: "MULTIPLEXING_HIGH"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	target := xnet.TCPDestination(xnet.LocalHostIP, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target}})
	upReader, upWriter := pipe.New()
	downReader, downWriter := pipe.New()
	defer upWriter.Close()
	defer downReader.Interrupt()
	done := make(chan error, 1)
	go func() {
		done <- client.Process(ctx, &transport.Link{Reader: upReader, Writer: downWriter}, &reviewCountingDialer{})
	}()
	if err := upWriter.WriteMultiBuffer(buf.MergeBytes(nil, []byte("pending-payload"))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("official server failed to authenticate and parse request")
	}
	limit := 1300 * time.Millisecond
	if cancelAfterAuth {
		cancel()
		limit = 500 * time.Millisecond
	}
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatal("native outbound exceeded cancellation/absolute handshake bound after partial SOCKS response")
	}
}

type reviewGatewayDialer struct {
	mu       sync.Mutex
	gateways []string
}

func (d *reviewGatewayDialer) Dial(ctx context.Context, target xnet.Destination) (stat.Connection, error) {
	outs := session.OutboundsFromContext(ctx)
	source := outs[len(outs)-1].Gateway
	conn, err := (&net.Dialer{LocalAddr: &net.TCPAddr{IP: source.IP()}}).DialContext(ctx, target.Network.SystemString(), target.NetAddr())
	if err == nil {
		d.mu.Lock()
		d.gateways = append(d.gateways, source.IP().String())
		d.mu.Unlock()
	}
	return conn, err
}
func (*reviewGatewayDialer) DestIpAddress() net.IP                                 { return nil }
func (*reviewGatewayDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func TestReviewPoolDoesNotShareDistinctSelectedGateways(t *testing.T) {
	port := reservePort(t, "TCP")
	referenceServer(t, port, "TCP")
	client, err := mieru.NewClient(context.Background(), &mieru.ClientConfig{Address: xnet.NewIPOrDomain(xnet.LocalHostIP), Port: uint32(port), Username: "alice", Password: "native-business-secret", Transport: "TCP", Multiplexing: "MULTIPLEXING_HIGH"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	d := &reviewGatewayDialer{}
	user := &protocol.MemoryUser{ClientID: nativeClientID, Email: "alice"}
	target := xnet.DestinationFromAddr(echoTarget(t, "tcp"))
	for i := 0; i < 12; i++ {
		gateway := net.IPv4(127, 0, 0, byte(i+2)).String()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ctx = session.ContextWithInbound(ctx, &session.Inbound{User: user, Tag: "one-inbound"})
		ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target, Gateway: xnet.ParseAddress(gateway)}})
		upReader, upWriter := pipe.New()
		downReader, downWriter := pipe.New()
		defer upWriter.Close()
		defer downReader.Interrupt()
		go func() { _ = client.Process(ctx, &transport.Link{Reader: upReader, Writer: downWriter}, d) }()
		if err := upWriter.WriteMultiBuffer(buf.MergeBytes(nil, []byte("gateway-payload"))); err != nil {
			t.Fatal(err)
		}
		mb, err := downReader.ReadMultiBufferTimeout(2 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		buf.ReleaseMulti(mb)
		d.mu.Lock()
		found := false
		for _, used := range d.gateways {
			if used == gateway {
				found = true
			}
		}
		d.mu.Unlock()
		if !found {
			t.Fatalf("request selected source gateway %s but reused physical transport created for a different gateway", gateway)
		}
	}
}

type reviewLateConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *reviewLateConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

type reviewLateDialer struct {
	started chan struct{}
	release chan struct{}
	conn    *reviewLateConn
}

func (d *reviewLateDialer) Dial(ctx context.Context, target xnet.Destination) (stat.Connection, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, target.Network.SystemString(), target.NetAddr())
	if err != nil {
		return nil, err
	}
	d.conn = &reviewLateConn{Conn: conn, closed: make(chan struct{})}
	close(d.started)
	<-d.release
	return d.conn, nil
}
func (*reviewLateDialer) DestIpAddress() net.IP                                 { return nil }
func (*reviewLateDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func TestReviewPoolCloseCannotLeakLateDialResult(t *testing.T) {
	port := reservePort(t, "UDP")
	referenceServer(t, port, "UDP")
	client, err := mieru.NewClient(context.Background(), &mieru.ClientConfig{Address: xnet.NewIPOrDomain(xnet.LocalHostIP), Port: uint32(port), Username: "alice", Password: "native-business-secret", Transport: "UDP", Multiplexing: "MULTIPLEXING_HIGH"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	d := &reviewLateDialer{started: make(chan struct{}), release: make(chan struct{})}
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: xnet.TCPDestination(xnet.LocalHostIP, 1)}})
	upReader, upWriter := pipe.New()
	downReader, downWriter := pipe.New()
	defer upWriter.Close()
	defer downReader.Interrupt()
	done := make(chan error, 1)
	go func() { done <- client.Process(ctx, &transport.Link{Reader: upReader, Writer: downWriter}, d) }()
	select {
	case <-d.started:
	case <-time.After(2 * time.Second):
		t.Fatal("supplied dialer failed to establish real socket")
	}
	defer d.conn.Close()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	close(d.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("late request did not terminate")
	}
	select {
	case <-d.conn.closed:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("successful dial result arriving after pool Close leaked outside already-stopped official mux")
	}
}
