package core_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman"
	inboundapp "github.com/xtls/xray-core/app/proxyman/inbound"
	outboundapp "github.com/xtls/xray-core/app/proxyman/outbound"
	statsapp "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/stats"
)

type trafficDrainer interface {
	TrafficDrainBootID() string
	CanDrainTraffic() bool
	DrainTraffic(context.Context, string) (map[string]int64, error)
}

type drainInbound struct {
	inbound.Handler
	close func() error
}

func (*drainInbound) Tag() string    { return "in" }
func (*drainInbound) Start() error   { return nil }
func (h *drainInbound) Close() error { return h.close() }

type drainOutbound struct {
	outbound.Handler
	close func() error
}

func (*drainOutbound) Tag() string    { return "out" }
func (*drainOutbound) Start() error   { return nil }
func (h *drainOutbound) Close() error { return h.close() }

func drainFixture(t *testing.T, statistics stats.Manager) (*core.Instance, trafficDrainer, *inboundapp.Manager, *outboundapp.Manager) {
	t.Helper()
	s := new(core.Instance)
	in, _ := inboundapp.New(context.Background(), &proxyman.InboundConfig{})
	out, _ := outboundapp.New(context.Background(), &proxyman.OutboundConfig{})
	for _, f := range []features.Feature{in, out, statistics} {
		if err := s.AddFeature(f); err != nil {
			t.Fatal(err)
		}
	}
	d, ok := any(s).(trafficDrainer)
	if !ok {
		t.Fatal("core has no boot-scoped final traffic drain")
	}
	return s, d, in, out
}

func TestTrafficDrainBootFenceAndUnsupported(t *testing.T) {
	m, _ := statsapp.NewManager(context.Background(), &statsapp.Config{})
	_, d, in, out := drainFixture(t, m)
	_, other, _, _ := drainFixture(t, stats.NoopManager{})
	boot := d.TrafficDrainBootID()
	if boot == "" || boot != d.TrafficDrainBootID() || boot == other.TrafficDrainBootID() {
		t.Fatal("boot identity is missing, unstable or reused")
	}
	if !d.CanDrainTraffic() || other.CanDrainTraffic() {
		t.Fatal("unsupported statistics advertised final accounting")
	}
	for _, wrong := range []string{"", other.TrafficDrainBootID()} {
		if counters, err := d.DrainTraffic(context.Background(), wrong); err == nil || counters != nil {
			t.Fatal("wrong boot accepted")
		}
	}
	if err := in.Start(); err != nil {
		t.Fatalf("wrong boot closed inbound: %v", err)
	}
	if err := out.Start(); err != nil {
		t.Fatalf("wrong boot closed outbound: %v", err)
	}
	if counters, err := other.DrainTraffic(context.Background(), other.TrafficDrainBootID()); err == nil || counters != nil {
		t.Fatal("noop statistics drained")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.DrainTraffic(ctx, boot); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	if err := in.Start(); err != nil {
		t.Fatalf("canceled request closed inbound: %v", err)
	}
	if err := out.Start(); err != nil {
		t.Fatalf("canceled request closed outbound: %v", err)
	}
}

func TestTrafficDrainWaitsForOwnersAndCounterLease(t *testing.T) {
	m, _ := statsapp.NewManager(context.Background(), &statsapp.Config{})
	s, d, in, out := drainFixture(t, m)
	counter, _ := m.RegisterCounter("user>>>alice>>>traffic>>>uplink")
	lease, err := stats.BeginIO(counter)
	if err != nil {
		t.Fatal(err)
	}
	inEntered, outEntered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var inCalls, outCalls atomic.Int32
	if err := in.AddHandler(context.Background(), &drainInbound{close: func() error { inCalls.Add(1); close(inEntered); <-release; return nil }}); err != nil {
		t.Fatal(err)
	}
	if err := out.AddHandler(context.Background(), &drainOutbound{close: func() error { outCalls.Add(1); close(outEntered); <-release; return nil }}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		values, err := d.DrainTraffic(ctx, d.TrafficDrainBootID())
		if values != nil {
			err = errors.New("premature counters returned")
		}
		result <- err
	}()
	for _, entered := range []chan struct{}{inEntered, outEntered} {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("one blocked owner prevented the other close")
		}
	}
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain deadline: %v", err)
	}
	if err := in.Start(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("inbound restarted: %v", err)
	}
	if err := out.Start(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("outbound restarted: %v", err)
	}
	close(release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if values, err := d.DrainTraffic(ctx2, d.TrafficDrainBootID()); !errors.Is(err, context.DeadlineExceeded) || values != nil {
		t.Fatalf("active IO acknowledged: %v %v", values, err)
	}
	counter.Add(123)
	lease.EndIO()
	ctx3, cancel3 := context.WithTimeout(context.Background(), time.Second)
	defer cancel3()
	values, err := d.DrainTraffic(ctx3, d.TrafficDrainBootID())
	if err != nil || values["user>>>alice>>>traffic>>>uplink"] != 123 {
		t.Fatalf("final counters: %v %v", values, err)
	}
	values["user>>>alice>>>traffic>>>uplink"] = 0
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := d.DrainTraffic(ctx3, d.TrafficDrainBootID())
	if err != nil || again["user>>>alice>>>traffic>>>uplink"] != 123 {
		t.Fatalf("retry lost immutable result: %v %v", again, err)
	}
	if inCalls.Load() != 1 || outCalls.Load() != 1 {
		t.Fatal("retry closed an owner twice")
	}
}

func TestTrafficDrainCloseErrorCannotAcknowledge(t *testing.T) {
	m, _ := statsapp.NewManager(context.Background(), &statsapp.Config{})
	_, d, _, out := drainFixture(t, m)
	want := errors.New("cannot terminate business connection")
	if err := out.AddHandler(context.Background(), &drainOutbound{close: func() error { return want }}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		values, err := d.DrainTraffic(ctx, d.TrafficDrainBootID())
		cancel()
		if err == nil || values != nil {
			t.Fatalf("failed owner acknowledged: %v %v", values, err)
		}
	}
}

type delayedDrainContext struct {
	context.Context
	checked, release chan struct{}
	once             sync.Once
}

func (c *delayedDrainContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked); <-c.release })
	return err
}

func TestTrafficDrainCancellationBeforeOperationDoesNotClose(t *testing.T) {
	m, _ := statsapp.NewManager(context.Background(), &statsapp.Config{})
	_, d, in, out := drainFixture(t, m)
	closed := make(chan struct{})
	if err := in.AddHandler(context.Background(), &drainInbound{close: func() error { close(closed); return nil }}); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &delayedDrainContext{Context: parent, checked: make(chan struct{}), release: make(chan struct{})}
	result := make(chan error, 1)
	go func() { _, err := d.DrainTraffic(ctx, d.TrafficDrainBootID()); result <- err }()
	<-ctx.checked
	cancel()
	close(ctx.release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	select {
	case <-closed:
		t.Fatal("canceled request started owned closure")
	case <-time.After(50 * time.Millisecond):
	}
	if err := in.Start(); err != nil {
		t.Fatalf("canceled request closed inbound: %v", err)
	}
	if err := out.Start(); err != nil {
		t.Fatalf("canceled request closed outbound: %v", err)
	}
}

func TestTrafficDrainFailedCloseDoesNotHideBehindActiveLease(t *testing.T) {
	m, _ := statsapp.NewManager(context.Background(), &statsapp.Config{})
	_, d, _, out := drainFixture(t, m)
	counter, _ := m.RegisterCounter("stuck-IO")
	lease, err := stats.BeginIO(counter)
	if err != nil {
		t.Fatal(err)
	}
	defer stats.EndIO(lease)
	want := errors.New("failed to terminate active IO")
	if err := out.AddHandler(context.Background(), &drainOutbound{close: func() error { return want }}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		values, err := d.DrainTraffic(ctx, d.TrafficDrainBootID())
		cancel()
		if err == nil || !strings.Contains(err.Error(), want.Error()) || values != nil {
			t.Fatalf("close failure hidden by lease: %v %v", values, err)
		}
	}
	if another, err := stats.BeginIO(counter); err == nil {
		stats.EndIO(another)
		t.Fatal("failed drain reopened IO")
	}
}
