package outbound

import (
	"context"
	"errors"
	gonet "net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
)

func TestPreconnectStopsWhenOutboundOwnerCloses(t *testing.T) {
	local, peer := gonet.Pipe()
	dialer := &closedOwnerDialer{retried: make(chan struct{}), release: make(chan struct{}), connection: local}
	handler := &Handler{testpre: 1, server: &protocol.ServerSpec{Destination: net.TCPDestination(net.LocalHostIP, 1)}}
	t.Cleanup(func() { _ = handler.Close(); close(dialer.release); _ = local.Close(); _ = peer.Close() })
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: net.TCPDestination(net.LocalHostIP, 2)}})
	stopped := make(chan error, 1)
	go func() { stopped <- handler.Process(ctx, &transport.Link{}, dialer) }()
	select {
	case err := <-stopped:
		if !errors.Is(err, gonet.ErrClosed) && !errors.Is(err, context.Canceled) {
			t.Fatalf("preconnect returned wrong shutdown error: %v", err)
		}
	case <-dialer.retried:
		t.Fatal("preconnect retried a permanently closed outbound owner")
	case <-time.After(time.Second):
		t.Fatal("preconnect kept waiting after owner closure")
	}
}

type closedOwnerDialer struct {
	calls            atomic.Int32
	retried, release chan struct{}
	connection       gonet.Conn
}

func (d *closedOwnerDialer) Dial(context.Context, net.Destination) (stat.Connection, error) {
	if d.calls.Add(1) == 1 {
		return nil, gonet.ErrClosed
	}
	close(d.retried)
	<-d.release
	return d.connection, nil
}
func (*closedOwnerDialer) DestIpAddress() net.IP                                 { return nil }
func (*closedOwnerDialer) SetOutboundGateway(context.Context, *session.Outbound) {}
