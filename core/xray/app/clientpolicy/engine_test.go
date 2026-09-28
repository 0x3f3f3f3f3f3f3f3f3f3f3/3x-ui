package clientpolicy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testPolicy(id string) Policy {
	return Policy{ClientID: id, Version: 1, Enabled: true, Multiplier: 1000000, BurstBytes: 65536}
}

func openSession(t *testing.T, e *Engine, id string, closeFn func()) *Session {
	t.Helper()
	s, err := e.Open(context.Background(), Metadata{ClientID: id, InboundTag: "owned-listener", AuthenticatedAccount: "server-binding", OriginalTarget: "tcp:127.0.0.1:80", ActualTarget: "tcp:127.0.0.1:80"}, closeFn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestUnknownOrRevokedIdentityCannotObtainAnUnlimitedSession(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	_, err := e.Open(context.Background(), Metadata{ClientID: "missing"}, nil)
	if !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("missing identity: %v", err)
	}
	p := testPolicy("client")
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, "client", nil)
	if err := e.Remove("client"); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(Upload, 1); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("revoked session admitted data: %v", err)
	}
	p.Version++
	if err := e.Apply(p); !errors.Is(err, ErrRevoked) {
		t.Fatalf("deleted identity reused: %v", err)
	}
}

func TestConcurrentSessionsCompeteForOneBidirectionalFractionalQuota(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("shared")
	p.Multiplier = 1500000
	p.QuotaBytes = 1536
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	var closed atomic.Int32
	sessions := make([]*Session, 16)
	for i := range sessions {
		sessions[i] = openSession(t, e, "shared", func() { closed.Add(1) })
	}
	var wg sync.WaitGroup
	var admitted atomic.Uint64
	for i, s := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 256; j++ {
				err := s.Admit(Direction(i%2), 1)
				if err != nil {
					if !errors.Is(err, ErrRestricted) && !errors.Is(err, ErrSessionClosed) {
						t.Errorf("admit: %v", err)
					}
					return
				}
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1024 {
		t.Fatalf("shared quota admitted %d raw bytes, want 1024", admitted.Load())
	}
	snap, err := e.Snapshot("shared")
	if err != nil || snap.Usage.RawUpload+snap.Usage.RawDownload != 1024 || snap.Usage.BilledBytes != 1536 || snap.Usage.Remainder != 0 || snap.Reasons != ReasonQuota {
		t.Fatalf("wrong shared accounting: %+v %v", snap, err)
	}
	if closed.Load() != 16 || snap.ActiveSessions != 0 {
		t.Fatalf("quota left active sessions: closed=%d snapshot=%+v", closed.Load(), snap)
	}
	_, err = e.Open(context.Background(), Metadata{ClientID: "shared"}, nil)
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("quota reconnect permitted: %v", err)
	}
}

func TestHotPolicyUpdatePreservesUsageAndClosesIdleConnections(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("client")
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	s := openSession(t, e, "client", func() {
		if _, err := e.Snapshot("client"); err != nil {
			t.Error(err)
		}
		close(closed)
	})
	if err := s.Admit(Upload, 10<<30); err != nil {
		t.Fatal(err)
	}
	p.Version = 2
	p.Multiplier = 2000000
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(Download, 5<<30); err != nil {
		t.Fatal(err)
	}
	p.Version = 3
	p.QuotaBytes = 19 << 30
	p.Enabled = false
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("policy update left idle connection alive")
	}
	snap, _ := e.Snapshot("client")
	if snap.Usage.BilledBytes != 20<<30 || snap.PolicyVersion != 3 || snap.Reasons != ReasonDisabled|ReasonQuota {
		t.Fatalf("bad policy boundary: %+v", snap)
	}
	p.Version++
	p.QuotaBytes = 30 << 30
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	snap, _ = e.Snapshot("client")
	if snap.Reasons != ReasonDisabled {
		t.Fatalf("quota top-up cleared manual disable: %+v", snap)
	}
	p.Version = 2
	if err := e.Apply(p); !errors.Is(err, ErrPolicyVersion) {
		t.Fatalf("stale update accepted: %v", err)
	}
}

func TestExpiryCancelsIdleSessionWithoutPanelPolling(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("expiring")
	p.ExpiresAt = time.Now().Add(100 * time.Millisecond).UnixMilli()
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	openSession(t, e, p.ClientID, func() { close(closed) })
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("idle session survived expiry")
	}
	snap, _ := e.Snapshot(p.ClientID)
	if snap.Reasons != ReasonExpired || snap.ActiveSessions != 0 {
		t.Fatalf("expiry state: %+v", snap)
	}
}

func TestSharedRateLimitsBothDirectionsAndWakesOnHotUpdate(t *testing.T) {
	for _, rate := range []uint64{32768, 65536} {
		e := NewEngine()
		p := testPolicy("limited")
		p.UploadRate = rate
		p.DownloadRate = rate
		p.BurstBytes = 4096
		if err := e.Apply(p); err != nil {
			t.Fatal(err)
		}
		a := openSession(t, e, p.ClientID, nil)
		b := openSession(t, e, p.ClientID, nil)
		start := time.Now()
		var wg sync.WaitGroup
		for _, s := range []*Session{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 8; i++ {
					if err := s.Admit(Upload, 4096); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		elapsed := time.Since(start)
		minimum := time.Duration(float64(65536-4096) / float64(rate) * float64(time.Second))
		if elapsed < minimum-10*time.Millisecond || elapsed > minimum+700*time.Millisecond {
			t.Fatalf("rate=%d elapsed=%v want %v..%v", rate, elapsed, minimum-10*time.Millisecond, minimum+700*time.Millisecond)
		}
		start = time.Now()
		if err := a.Admit(Download, 4096); err != nil {
			t.Fatal(err)
		}
		if time.Since(start) > 100*time.Millisecond {
			t.Fatal("upload consumed download tokens")
		}
		done := make(chan error, 1)
		go func() { done <- a.Admit(Upload, 4096) }()
		p.Version++
		p.UploadRate = 0
		if err := e.Apply(p); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("old rate wait survived unlimited update")
		}
		e.Close()
	}
}

func TestRejectedDatagramIsNotPartiallyCharged(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("udp")
	p.QuotaBytes = 10
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 11); !errors.Is(err, ErrRestricted) {
		t.Fatalf("oversized quota admission: %v", err)
	}
	snap, _ := e.Snapshot(p.ClientID)
	if snap.Usage != (Usage{}) {
		t.Fatalf("partially charged datagram: %+v", snap)
	}
}

func TestFractionalBudgetThatCannotBuyOneByteIsExhausted(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("fraction")
	p.Multiplier = 1500000
	p.QuotaBytes = 4
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	closed := false
	s := openSession(t, e, p.ClientID, func() { closed = true })
	if err := s.Admit(Upload, 2); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Snapshot(p.ClientID)
	if snap.Usage.BilledBytes != 3 || snap.Reasons != ReasonQuota || !closed {
		t.Fatalf("unusable fractional remainder left client active: %+v, closed=%v", snap, closed)
	}
}

func TestNaturalChannelReleaseDoesNotTerminateSharedTransport(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("multiplexed")
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	var closed atomic.Int32
	s := openSession(t, e, p.ClientID, func() { closed.Add(1) })
	other := openSession(t, e, p.ClientID, func() { closed.Add(1) })
	s.Release()
	if err := other.Admit(Upload, 128); err != nil {
		t.Fatal(err)
	}
	snap, _ := e.Snapshot(p.ClientID)
	if closed.Load() != 0 || snap.ActiveSessions != 1 {
		t.Fatalf("normal channel release killed shared transport: %+v closed=%d", snap, closed.Load())
	}
	p.Version++
	p.Enabled = false
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatalf("remaining channel not terminated: %d", closed.Load())
	}
}
