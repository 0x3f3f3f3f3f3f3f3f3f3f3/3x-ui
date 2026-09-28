package clientpolicy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestLimiterValidatesRatesAndUsesBoundedGrants(t *testing.T) {
	for _, rate := range []int64{-1, MaxRate + 1} {
		if _, err := NewLimiter(rate); !errors.Is(err, ErrInvalidRate) {
			t.Fatalf("accepted rate %d: %v", rate, err)
		}
	}
	l, err := NewLimiter(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []int{0, -1} {
		if _, err := l.Acquire(context.Background(), request); !errors.Is(err, ErrInvalidGrant) {
			t.Fatalf("accepted nonpositive grant request: %v", err)
		}
	}
	for range 10 {
		if n, err := l.Acquire(context.Background(), 1<<20); err != nil || n != MaxGrant {
			t.Fatalf("unbounded or throttled unlimited grant: %d, %v", n, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Acquire(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled flow obtained a grant: %v", err)
	}
}

func TestLimiterRateChangeWakesExistingWaiter(t *testing.T) {
	l, err := NewLimiter(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := l.Acquire(ctx, 1); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("second byte bypassed the one-byte burst: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	start := time.Now()
	if err := l.SetRate(0); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil || time.Since(start) > 250*time.Millisecond {
			t.Fatalf("existing waiter ignored live rate update: %v after %v", err, time.Since(start))
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("rate update did not wake existing waiter")
	}
}

func TestLimiterInvalidUpdatePreservesOldRate(t *testing.T) {
	l, err := NewLimiter(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SetRate(-1); !errors.Is(err, ErrInvalidRate) {
		t.Fatalf("accepted invalid update: %v", err)
	}
	if _, err := l.Acquire(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("invalid update changed the active cap: %v", err)
	}
}

func TestLimiterCancellationDoesNotBlockFollowingFlow(t *testing.T) {
	l, err := NewLimiter(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting flow was not canceled: %v", err)
	}
	if err := l.SetRate(0); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if n, err := l.Acquire(ctx2, 37); err != nil || n != 37 {
		t.Fatalf("canceled head blocked following flow: %d, %v", n, err)
	}
}

func TestLimiterAggregateAcrossConcurrentWriters(t *testing.T) {
	l, err := NewLimiter(64 << 10)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	start := time.Now()
	for range 4 {
		wg.Go(func() {
			w := NewShapedWriter(ctx, io.Discard, l)
			if n, err := w.Write(make([]byte, 32<<10)); err != nil || n != 32<<10 {
				t.Errorf("shaped write: %d, %v", n, err)
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	if elapsed < 1800*time.Millisecond || elapsed > 3500*time.Millisecond {
		t.Fatalf("128 KiB at shared 64 KiB/s with <=100ms burst took %v", elapsed)
	}
}

func TestLimiterBoundsQueueAndReclaimsCanceledWaiters(t *testing.T) {
	l, err := NewLimiter(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	results := make(chan error, 2*maxWaiters)
	for range 2 * maxWaiters {
		go func() { _, err := l.Acquire(ctx, 1); results <- err }()
	}
	busy, canceled := 0, 0
	for range 2 * maxWaiters {
		switch err := <-results; {
		case errors.Is(err, ErrShaperBusy):
			busy++
		case errors.Is(err, context.DeadlineExceeded):
			canceled++
		default:
			t.Fatalf("unexpected queued grant result: %v", err)
		}
	}
	if busy == 0 || canceled > maxWaiters {
		t.Fatalf("queue admitted unbounded waits: full=%d canceled=%d", busy, canceled)
	}
	if err := l.SetRate(0); err != nil {
		t.Fatal(err)
	}
	if n, err := l.Acquire(context.Background(), 1); err != nil || n != 1 {
		t.Fatalf("canceled queue retained entries: %d, %v", n, err)
	}
}

func TestLimiterRepeatedUpdatesDoNotMintBurst(t *testing.T) {
	l, err := NewLimiter(64 << 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(context.Background(), MaxGrant); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if err := l.SetRate(64 << 10); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(ctx, MaxGrant); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repeated policy writes refilled the burst: %v", err)
	}
}

func TestShapedWriterPreservesPayloadAndShortWrite(t *testing.T) {
	l, err := NewLimiter(0)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("payload"), 10000)
	var dst bytes.Buffer
	w := NewShapedWriter(context.Background(), &dst, l)
	if n, err := w.Write(payload); err != nil || n != len(payload) || !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("shaping corrupted stream: %d, %v", n, err)
	}
	// A fixed-size slice writer exposes partial writes without accepting the remaining payload.
	short := &fixedWriter{remaining: 17}
	w = NewShapedWriter(context.Background(), short, l)
	if n, err := w.Write(payload); n != 17 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write lost its byte count/error: %d, %v", n, err)
	}
}

type fixedWriter struct{ remaining int }

func (w *fixedWriter) Write(p []byte) (int, error) {
	n := min(w.remaining, len(p))
	w.remaining -= n
	return n, nil
}
