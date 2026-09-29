package outbound

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman"
	xerrors "github.com/xtls/xray-core/common/errors"
	feature "github.com/xtls/xray-core/features/outbound"
)

type managerTestHandler struct {
	feature.Handler
	tag            string
	close          func() error
	startErr       error
	starts, closes atomic.Int32
}

func (h *managerTestHandler) Tag() string  { return h.tag }
func (h *managerTestHandler) Start() error { h.starts.Add(1); return h.startErr }
func (h *managerTestHandler) Close() error {
	h.closes.Add(1)
	if h.close != nil {
		return h.close()
	}
	return nil
}

func TestOutboundManagerRemovalOwnsHandlerClose(t *testing.T) {
	m, _ := New(context.Background(), &proxyman.OutboundConfig{})
	h := &managerTestHandler{tag: "removed"}
	if err := m.AddHandler(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveHandler(context.Background(), h.tag); err != nil {
		t.Fatal(err)
	}
	if h.closes.Load() != 1 {
		t.Fatalf("removed handler closed %d times", h.closes.Load())
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if h.closes.Load() != 1 {
		t.Fatal("manager closed removed handler twice")
	}
}

func TestOutboundManagerCloseSealsAdmissionAndSelection(t *testing.T) {
	m, _ := New(context.Background(), &proxyman.OutboundConfig{})
	h := &managerTestHandler{tag: "one"}
	_ = m.AddHandler(context.Background(), h)
	_ = m.Start()
	_ = m.Close()
	if m.GetHandler(h.tag) != nil || m.GetDefaultHandler() != nil || len(m.ListHandlers(context.Background())) != 0 || len(m.Select([]string{"o"})) != 0 {
		t.Fatal("closed manager still selects handlers")
	}
	if err := m.Start(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed manager Start: %v", err)
	}
	late := &managerTestHandler{tag: "late"}
	if err := m.AddHandler(context.Background(), late); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed manager Add: %v", err)
	}
	if late.starts.Load() != 0 {
		t.Fatal("closed manager started a new handler")
	}
}

func TestOutboundManagerCloseAllowsReentrantLookup(t *testing.T) {
	m, _ := New(context.Background(), &proxyman.OutboundConfig{})
	h := &managerTestHandler{tag: "one"}
	h.close = func() error {
		looked := make(chan struct{})
		go func() { _ = m.GetHandler("one"); close(looked) }()
		select {
		case <-looked:
			return nil
		case <-time.After(time.Second):
			return errors.New("manager held lookup lock across handler Close")
		}
	}
	_ = m.AddHandler(context.Background(), h)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboundManagerCloseWaitsForRemovedHandler(t *testing.T) {
	m, _ := New(context.Background(), &proxyman.OutboundConfig{})
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h := &managerTestHandler{tag: "removed", close: func() error { close(entered); <-release; return nil }}
	_ = m.AddHandler(context.Background(), h)
	removed := make(chan error, 1)
	go func() { removed <- m.RemoveHandler(context.Background(), h.tag) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("removal did not start handler close")
	}
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("manager lost retiring handler: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if h.closes.Load() != 1 {
		t.Fatalf("concurrent close invoked handler %d times", h.closes.Load())
	}
	// Release and join through cleanup, including failed assertions above.
	t.Cleanup(func() { <-removed; <-closed })
}

func TestOutboundManagerCloseRetainsRemovalFailure(t *testing.T) {
	m, _ := New(context.Background(), &proxyman.OutboundConfig{})
	want := errors.New("handler close failed")
	h := &managerTestHandler{tag: "removed", close: func() error { return want }}
	_ = m.AddHandler(context.Background(), h)
	if err := m.RemoveHandler(context.Background(), h.tag); !xerrors.AllEqual(want, err) {
		t.Fatalf("removal lost close failure: %v", err)
	}
	for range 2 {
		if err := m.Close(); !xerrors.AllEqual(want, err) {
			t.Fatalf("manager lost close failure: %v", err)
		}
	}
	if h.closes.Load() != 1 {
		t.Fatalf("failed handler closed %d times", h.closes.Load())
	}
}

func TestOutboundManagerFailedAddDoesNotPublishHandler(t *testing.T) {
	m, _ := New(context.Background(), &proxyman.OutboundConfig{})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	want := errors.New("start failed")
	h := &managerTestHandler{tag: "failed", startErr: want}
	if err := m.AddHandler(context.Background(), h); !errors.Is(err, want) {
		t.Fatalf("failed start returned %v", err)
	}
	if len(m.ListHandlers(context.Background())) != 0 {
		t.Fatal("failed start published handler")
	}
	_ = h.Close()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if h.closes.Load() != 1 {
		t.Fatal("manager retained rejected handler ownership")
	}
}

func TestOutboundManagerRemovalRemainsIdempotentAfterClose(t *testing.T) {
	m, _ := New(context.Background(), &proxyman.OutboundConfig{})
	_ = m.Close()
	if err := m.RemoveHandler(context.Background(), "reverse-portal"); err != nil {
		t.Fatalf("feature shutdown cannot remove an already closed outbound: %v", err)
	}
}
