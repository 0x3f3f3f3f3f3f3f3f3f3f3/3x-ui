package core_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/proxy"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type rejectedProxyContextKey struct{}

type rejectedInboundProxy struct {
	proxy.Inbound
	closed atomic.Int32
}

func (*rejectedInboundProxy) Network() []xnet.Network { return nil }
func (p *rejectedInboundProxy) Close() error          { p.closed.Add(1); return nil }

type rejectedOutboundProxy struct {
	proxy.Outbound
	closed atomic.Int32
}

func (p *rejectedOutboundProxy) Close() error { p.closed.Add(1); return nil }

type rejectingInboundManager struct{ inbound.Manager }

func (*rejectingInboundManager) Type() interface{} { return inbound.ManagerType() }
func (*rejectingInboundManager) Start() error      { return nil }
func (*rejectingInboundManager) Close() error      { return nil }
func (*rejectingInboundManager) AddHandler(context.Context, inbound.Handler) error {
	return net.ErrClosed
}

type rejectingOutboundManager struct{ outbound.Manager }

func (*rejectingOutboundManager) Type() interface{} { return outbound.ManagerType() }
func (*rejectingOutboundManager) Start() error      { return nil }
func (*rejectingOutboundManager) Close() error      { return nil }
func (*rejectingOutboundManager) AddHandler(context.Context, outbound.Handler) error {
	return net.ErrClosed
}

func init() {
	common.Must(common.RegisterConfig((*emptypb.Empty)(nil), func(ctx context.Context, _ interface{}) (interface{}, error) {
		return ctx.Value(rejectedProxyContextKey{}), nil
	}))
	common.Must(common.RegisterConfig((*wrapperspb.BoolValue)(nil), func(ctx context.Context, _ interface{}) (interface{}, error) {
		return ctx.Value(rejectedProxyContextKey{}), nil
	}))
}

func TestRejectedHandlerConstructionClosesProxy(t *testing.T) {
	t.Run("inbound", func(t *testing.T) {
		resource := &rejectedInboundProxy{}
		instance, err := core.NewWithContext(context.WithValue(context.Background(), rejectedProxyContextKey{}, resource), &core.Config{})
		if err != nil {
			t.Fatal(err)
		}
		defer instance.Close()
		if err := instance.AddFeature(&rejectingInboundManager{}); err != nil {
			t.Fatal(err)
		}
		err = core.AddInboundHandler(instance, &core.InboundHandlerConfig{Tag: "rejected", ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{}), ProxySettings: serial.ToTypedMessage(&emptypb.Empty{})})
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("admission error: %v", err)
		}
		if resource.closed.Load() != 1 {
			t.Fatalf("rejected inbound proxy closed %d times", resource.closed.Load())
		}
	})
	t.Run("outbound", func(t *testing.T) {
		resource := &rejectedOutboundProxy{}
		instance, err := core.NewWithContext(context.WithValue(context.Background(), rejectedProxyContextKey{}, resource), &core.Config{})
		if err != nil {
			t.Fatal(err)
		}
		defer instance.Close()
		if err := instance.AddFeature(&rejectingOutboundManager{}); err != nil {
			t.Fatal(err)
		}
		err = core.AddOutboundHandler(instance, &core.OutboundHandlerConfig{Tag: "rejected", ProxySettings: serial.ToTypedMessage(&wrapperspb.BoolValue{})})
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("admission error: %v", err)
		}
		if resource.closed.Load() != 1 {
			t.Fatalf("rejected outbound proxy closed %d times", resource.closed.Load())
		}
	})
}
