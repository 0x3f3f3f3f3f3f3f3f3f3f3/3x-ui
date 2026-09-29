package inbound

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman"
	manager "github.com/xtls/xray-core/app/proxyman/outbound"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/uuid"
	feature "github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/proxy/vless"
)

type reverseTestOutbound struct {
	feature.Handler
	tag string
}

func (h *reverseTestOutbound) Tag() string { return h.tag }
func (*reverseTestOutbound) Start() error  { return nil }
func (*reverseTestOutbound) Close() error  { return nil }

type reverseTestManager struct {
	*manager.Manager
	release atomic.Bool
}

func (m *reverseTestManager) ListHandlers(ctx context.Context) []feature.Handler {
	if m.release.Load() {
		return []feature.Handler{&reverseTestOutbound{tag: "test cleanup"}}
	}
	return m.Manager.ListHandlers(ctx)
}

func newReverseOwnerTest(t *testing.T, m feature.Manager) (*Handler, *vless.MemoryAccount) {
	t.Helper()
	a := &vless.MemoryAccount{ID: protocol.NewID(uuid.New()), Reverse: &vless.Reverse{Tag: "reverse"}}
	validator := new(vless.MemoryValidator)
	if err := validator.Add(&protocol.MemoryUser{Email: "reverse@example.test", Account: a}); err != nil {
		t.Fatal(err)
	}
	return &Handler{ctx: context.Background(), outboundHandlerManager: m, validator: validator}, a
}

func TestVLESSReverseAdmissionWithoutDefaultDoesNotWait(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "closed"}[closed], func(t *testing.T) {
			base, _ := manager.New(context.Background(), &proxyman.OutboundConfig{})
			m := &reverseTestManager{Manager: base}
			defer base.Close()
			if closed {
				_ = base.Close()
			}
			h, a := newReverseOwnerTest(t, m)
			defer h.Close()
			done := make(chan error, 1)
			go func() { _, err := h.GetReverse(a); done <- err }()
			select {
			case err := <-done:
				if closed && !errors.Is(err, net.ErrClosed) {
					t.Fatalf("closed reverse admission: %v", err)
				}
				if !closed && err != nil {
					t.Fatal(err)
				}
			case <-time.After(200 * time.Millisecond):
				m.release.Store(true)
				<-done
				t.Fatal("reverse admission waits indefinitely for outbound selection")
			}
			if m.GetDefaultHandler() != nil {
				t.Fatal("reverse became default outbound")
			}
		})
	}
}

func TestVLESSUnadmittedHandlerDoesNotRemoveReverseOwner(t *testing.T) {
	m, _ := manager.New(context.Background(), &proxyman.OutboundConfig{})
	defer m.Close()
	_ = m.AddHandler(context.Background(), &reverseTestOutbound{tag: "direct"})
	owner, a := newReverseOwnerTest(t, m)
	defer owner.Close()
	r, err := owner.GetReverse(a)
	if err != nil {
		t.Fatal(err)
	}
	rejected, _ := newReverseOwnerTest(t, m)
	if err := rejected.Close(); err != nil {
		t.Fatal(err)
	}
	if m.GetHandler("reverse") != r {
		t.Fatal("unadmitted handler removed another handler's reverse route")
	}
}

func TestVLESSReverseOwnerClosePreservesReplacement(t *testing.T) {
	m, _ := manager.New(context.Background(), &proxyman.OutboundConfig{})
	defer m.Close()
	_ = m.AddHandler(context.Background(), &reverseTestOutbound{tag: "direct"})
	owner, a := newReverseOwnerTest(t, m)
	if _, err := owner.GetReverse(a); err != nil {
		t.Fatal(err)
	}
	_ = m.RemoveHandler(context.Background(), "reverse")
	replacement := &reverseTestOutbound{tag: "reverse"}
	_ = m.AddHandler(context.Background(), replacement)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if m.GetHandler("reverse") != replacement {
		t.Fatal("stale owner removed replacement reverse route")
	}
	if _, err := owner.GetReverse(a); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed reverse owner admitted request: %v", err)
	}
}
