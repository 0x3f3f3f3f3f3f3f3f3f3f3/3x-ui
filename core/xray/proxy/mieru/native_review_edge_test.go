package mieru_test

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	miProtocol "github.com/enfein/mieru/v3/pkg/protocol"
	miError "github.com/enfein/mieru/v3/pkg/stderror"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/proxy/mieru"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/pipe"
	"google.golang.org/protobuf/proto"
)

func reviewRawMux(t *testing.T, port int) *miProtocol.Mux {
	t.Helper()
	mux, err := appctlcommon.NewClientMuxFromProfile(&appctlpb.ClientProfile{
		User:    &appctlpb.User{Name: proto.String("alice"), Password: proto.String("native-business-secret")},
		Servers: []*appctlpb.ServerEndpoint{{IpAddress: proto.String("127.0.0.1"), PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: appctlpb.TransportProtocol_UDP.Enum()}}}},
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mux.Close() })
	return mux
}

func TestReviewUDPPartialHandshakeCannotDefeatTimeout(t *testing.T) {
	port := reservePort(t, "UDP")
	nativeCoreConfigured(t, port, "UDP", func(config map[string]any) {
		settings := config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
		settings["handshakeTimeoutSeconds"], settings["maxConnections"] = 1, 1
	})
	mux := reviewRawMux(t, port)
	conn, err := mux.DialContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(2 * time.Second)
	<-timer.C
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err = conn.Read(make([]byte, 1))
	if miError.IsTimeout(err) {
		t.Fatal("UDP partial SOCKS header survived configured one-second handshake timeout; session still open after two seconds")
	}
	if err == nil {
		t.Fatal("unexpected response")
	}
}

func TestReviewUDPFailedHandshakeMustReleaseCapacity(t *testing.T) {
	port := reservePort(t, "UDP")
	nativeCoreConfigured(t, port, "UDP", func(config map[string]any) {
		settings := config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
		settings["handshakeTimeoutSeconds"], settings["maxConnections"] = 1, 1
	})
	mux := reviewRawMux(t, port)
	conn, err := mux.DialContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write([]byte{0, 1, 0}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	_, err = conn.Read(make([]byte, 1))
	_ = conn.Close()
	if err == nil {
		t.Fatal("invalid SOCKS version accepted")
	}
	if miError.IsTimeout(err) {
		t.Fatal("invalid handshake was not rejected")
	}
	client := referenceClient(t, port, "UDP")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = client.Stop() })
	defer stop()
	good, err := client.DialContext(ctx, echoTarget(t, "tcp"))
	if err != nil {
		t.Fatalf("valid request remains blocked after failed handshake consumed sole packet binding capacity: %v", err)
	}
	defer good.Close()
	exchangeNative(t, good, "post-invalid-request")
}

type reviewCountingDialer struct{ count atomic.Int32 }

func (d *reviewCountingDialer) Dial(ctx context.Context, target xnet.Destination) (stat.Connection, error) {
	d.count.Add(1)
	return (&net.Dialer{}).DialContext(ctx, target.Network.SystemString(), target.NetAddr())
}
func (*reviewCountingDialer) DestIpAddress() net.IP                                 { return nil }
func (*reviewCountingDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func TestReviewOutboundHighMultiplexingSharesTransport(t *testing.T) {
	port := reservePort(t, "TCP")
	referenceServer(t, port, "TCP")
	client, err := mieru.NewClient(context.Background(), &mieru.ClientConfig{Address: xnet.NewIPOrDomain(xnet.LocalHostIP), Port: uint32(port), Username: "alice", Password: "native-business-secret", Transport: "TCP", Multiplexing: "MULTIPLEXING_HIGH"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	d := &reviewCountingDialer{}
	target := xnet.DestinationFromAddr(echoTarget(t, "tcp"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target}})
	for i := 0; i < 12; i++ {
		upReader, upWriter := pipe.New()
		downReader, downWriter := pipe.New()
		t.Cleanup(func() { _ = upWriter.Close(); downReader.Interrupt() })
		go func() { _ = client.Process(ctx, &transport.Link{Reader: upReader, Writer: downWriter}, d) }()
		payload := []byte(fmt.Sprintf("stream%d", i))
		if err := upWriter.WriteMultiBuffer(buf.MergeBytes(nil, payload)); err != nil {
			t.Fatal(err)
		}
		mb, err := downReader.ReadMultiBufferTimeout(2 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		buf.ReleaseMulti(mb)
	}
	if n := d.count.Load(); n >= 12 {
		t.Fatalf("HIGH multiplexing opened %d independent physical transports for twelve overlapping native outbound requests", n)
	}
}

func TestReviewOutboundClientCloseReleasesQuietPeer(t *testing.T) {
	port := reservePort(t, "TCP")
	referenceServer(t, port, "TCP")
	client, err := mieru.NewClient(context.Background(), &mieru.ClientConfig{Address: xnet.NewIPOrDomain(xnet.LocalHostIP), Port: uint32(port), Username: "alice", Password: "native-business-secret", Transport: "TCP", Multiplexing: "MULTIPLEXING_OFF"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	d := &reviewCountingDialer{}
	target := xnet.DestinationFromAddr(echoTarget(t, "tcp"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target}})
	upReader, upWriter := pipe.New()
	downReader, downWriter := pipe.New()
	done := make(chan error, 1)
	go func() { done <- client.Process(ctx, &transport.Link{Reader: upReader, Writer: downWriter}, d) }()
	if err := upWriter.WriteMultiBuffer(buf.MergeBytes(nil, []byte("before-close"))); err != nil {
		t.Fatal(err)
	}
	mb, err := downReader.ReadMultiBufferTimeout(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	buf.ReleaseMulti(mb)
	_ = upWriter.Close()
	downReader.Interrupt()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatal("closing both incoming Xray pipe endpoints left native outbound Process/transport waiting forever for quiet remote peer")
	}
}
