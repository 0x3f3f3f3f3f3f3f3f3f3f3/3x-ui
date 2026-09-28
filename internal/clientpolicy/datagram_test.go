package clientpolicy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDatagramGrantSharesDebtWithStreamsAndLiveRateChanges(t *testing.T) {
	limiter, err := NewLimiter(10000)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if n, err := limiter.AcquireDatagram(ctx, 6000); err != nil || n != 6000 {
		t.Fatalf("whole packet grant = %d, %v; want 6000 without fragmentation", n, err)
	}
	blocked, stop := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer stop()
	if n, err := limiter.Acquire(blocked, 1); n != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream bypassed shared packet debt: %d, %v", n, err)
	}
	packetDone := make(chan error, 1)
	go func() {
		n, err := limiter.AcquireDatagram(ctx, 6000)
		if err == nil && n != 6000 {
			err = ErrInvalidGrant
		}
		packetDone <- err
	}()
	select {
	case err := <-packetDone:
		t.Fatalf("second packet bypassed shared debt: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := limiter.SetRate(0); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-packetDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("live unlimited rate did not wake queued datagram")
	}
}
