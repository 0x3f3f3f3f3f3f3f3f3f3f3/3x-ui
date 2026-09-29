package stats_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/dispatcher"
	appstats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/buf"
)

func TestCopyFreezeBetweenCountersReleasesEarlierLease(t *testing.T) {
	first, _ := appstats.NewManager(context.Background(), &appstats.Config{})
	second, _ := appstats.NewManager(context.Background(), &appstats.Config{})
	c1, _ := first.GetOrRegisterCounter("first")
	c2, _ := second.GetOrRegisterCounter("second")
	if _, err := second.SealCounters(context.Background()); err != nil {
		t.Fatal(err)
	}
	source := bytes.NewReader([]byte("abc"))
	if err := buf.Copy(buf.NewReader(source), buf.Discard, buf.AddToStatCounter(c1), buf.AddToStatCounter(c2)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("copy accepted a sealed secondary counter: %v", err)
	}
	if source.Len() != 3 {
		t.Fatal("copy read bytes before acquiring all counters")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	final, err := first.SealCounters(ctx)
	if err != nil || final["first"] != 0 {
		t.Fatalf("failed multi-counter admission leaked a lease: %v %v", final, err)
	}
}

func TestNestedCopyFreezeRejectsDownstreamWithoutDeadlock(t *testing.T) {
	manager, _ := appstats.NewManager(context.Background(), &appstats.Config{})
	counter, _ := manager.GetOrRegisterCounter("nested")
	left, right := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	paused := &pausedAccountingConn{Conn: left, completed: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(paused.release) }) }
	t.Cleanup(unblock)
	var output bytes.Buffer
	writer := &dispatcher.SizeStatWriter{Counter: counter, Writer: buf.NewWriter(&output)}
	done := make(chan error, 1)
	go func() { done <- buf.Copy(buf.NewReader(paused), writer, buf.AddToStatCounter(counter)) }()
	_ = right.SetDeadline(time.Now().Add(time.Second))
	if _, err := right.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	<-paused.completed
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err := manager.SealCounters(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("freeze overtook active copy: %v", err)
	}
	unblock()
	if err := <-done; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("nested writer ignored sealed admission: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	final, err := manager.SealCounters(ctx)
	if err != nil || final["nested"] != 3 || output.Len() != 0 {
		t.Fatalf("nested accounting or release: %v %v, written=%d", final, err, output.Len())
	}
}
