package outbound

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
)

type rewrappingOutboundProxy struct{ started chan struct{} }

func (p *rewrappingOutboundProxy) Process(ctx context.Context, link *transport.Link, _ internet.Dialer) error {
	close(p.started)
	<-ctx.Done()
	link.Reader = &buf.TimeoutWrapperReader{Reader: link.Reader}
	link.Writer = &buf.EndpointOverrideWriter{Writer: link.Writer}
	return ctx.Err()
}

func TestOutboundCancellationCanOverlapRedispatchWrapping(t *testing.T) {
	for range 32 {
		started, stopped := make(chan struct{}), make(chan struct{})
		handler := &Handler{proxy: &rewrappingOutboundProxy{started: started}}
		ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: net.TCPDestination(net.LocalHostIP, 1)}})
		go func() {
			handler.Dispatch(ctx, &transport.Link{Reader: buf.NewReader(bytes.NewReader(nil)), Writer: buf.Discard})
			close(stopped)
		}()
		<-started
		if err := handler.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("cancelled redispatch stayed active")
		}
	}
}
