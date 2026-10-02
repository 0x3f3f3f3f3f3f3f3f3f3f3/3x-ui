package clientpolicy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenDuringControlledGrantHandoffWaitsForReplacement(t *testing.T) {
	e, g := authorityExecutionFixture(t, testPolicy("owner"), 100, time.Second)
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PauseAuthorityGrant(g.ClientID, g.GrantID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		s   *Session
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := e.Open(context.Background(), Metadata{ClientID: g.ClientID}, nil)
		done <- result{s, err}
	}()
	select {
	case got := <-done:
		if got.s != nil {
			got.s.Release()
		}
		t.Fatalf("connection entered sealed handoff: %v", got.err)
	case <-time.After(20 * time.Millisecond):
	}
	challenge, err := e.BeginAuthorityChallenge(g.BootID)
	if err != nil {
		t.Fatal(err)
	}
	next := g
	next.GrantID = "grant-2"
	next.Sequence = 2
	next.ChallengeID = challenge.ChallengeID
	if _, err := e.InstallAuthorityGrant(next); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.s == nil {
			t.Fatalf("handoff connection failed: %v", got.err)
		}
		defer got.s.Release()
		if err := got.s.Admit(Upload, 1); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("handoff connection did not resume")
	}
}

func TestOpenDuringControlledGrantHandoffHonorsCancellation(t *testing.T) {
	e, g := authorityExecutionFixture(t, testPolicy("owner"), 100, time.Second)
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PauseAuthorityGrant(g.ClientID, g.GrantID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		s, err := e.Open(ctx, Metadata{ClientID: g.ClientID}, nil)
		if s != nil {
			s.Release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("did not wait for controlled handoff: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handoff cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled connection wait remained pending")
	}
	state, err := e.Snapshot(g.ClientID)
	if err != nil || state.ActiveSessions != 0 || state.Usage.RawUpload != 0 {
		t.Fatalf("cancelled handoff created traffic/session: %+v/%v", state, err)
	}
}

func TestControlledGrantHandoffBoundsPendingConnections(t *testing.T) {
	e, g := authorityExecutionFixture(t, testPolicy("owner"), 100, time.Second)
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PauseAuthorityGrant(g.ClientID, g.GrantID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, maxPendingAdmissions+1)
	for range maxPendingAdmissions + 1 {
		go func() {
			s, err := e.Open(ctx, Metadata{ClientID: g.ClientID}, nil)
			if s != nil {
				s.Release()
			}
			done <- err
		}()
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrQueueFull) {
			t.Fatalf("pending connection bound: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending connection queue did not reject overflow")
	}
	cancel()
	for range maxPendingAdmissions {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("pending cancellation: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("pending connection did not cancel")
		}
	}
	c, err := e.state(g.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingOpens != 0 || len(c.sessions) != 0 || c.usage != (Usage{}) {
		t.Fatalf("pending connections retained resources/usage: pending%d sessions%d usage%+v", c.pendingOpens, len(c.sessions), c.usage)
	}
}

func TestSealedGrantHandoffPreservesSessionAndMultiplierBoundary(t *testing.T) {
	p := testPolicy("owner")
	e, g := authorityExecutionFixture(t, p, 100, time.Second)
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	var closed atomic.Int32
	s := openSession(t, e, p.ClientID, func() { closed.Add(1) })
	if err := s.Admit(Upload, 3); err != nil {
		t.Fatal(err)
	}
	seal, err := e.PauseAuthorityGrant(p.ClientID, g.GrantID)
	if err != nil || !seal.Sealed || seal.Usage.BilledBytes != 3 {
		t.Fatalf("pause boundary: %+v/%v", seal, err)
	}
	if closed.Load() != 0 {
		t.Fatal("bounded grant handoff closed existing session")
	}
	pending := make(chan error, 1)
	go func() { pending <- s.Admit(Upload, 4) }()
	select {
	case err := <-pending:
		t.Fatalf("sealed grant admitted before replacement: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	p.Version, p.Multiplier = 2, 2000000
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 0 {
		t.Fatal("hot policy update closed paused existing session")
	}
	challenge, err := e.BeginAuthorityChallenge(g.BootID)
	if err != nil {
		t.Fatal(err)
	}
	next := g
	next.PolicyVersion = p.Version
	next.GrantID = "grant-2"
	next.Sequence = 2
	next.ChallengeID = challenge.ChallengeID
	next.WindowID = "window-2"
	if _, err := e.InstallAuthorityGrant(next); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-pending:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement did not resume pending payload")
	}
	state, err := e.Snapshot(p.ClientID)
	if err != nil || state.ActiveSessions != 1 || state.Usage.RawUpload != 7 || state.Usage.BilledBytes != 11 || closed.Load() != 0 {
		t.Fatalf("handoff changed billing/session: %+v/%v/%d", state, err, closed.Load())
	}
}

func TestPausedGrantStillExpiresAndRejectsStreamChunks(t *testing.T) {
	e, g := authorityExecutionFixture(t, testPolicy("owner"), 100, 80*time.Millisecond)
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, g.ClientID, nil)
	if _, err := e.PauseAuthorityGrant(g.ClientID, g.GrantID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := s.StreamChunkSize(Upload); result <- err }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("sealed transition exposed stream allowance")
		}
		t.Fatal("transition returned before bounded deadline")
	case <-time.After(15 * time.Millisecond):
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expired transition admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("transition wait outlived grant deadline")
	}
	state, err := e.Snapshot(g.ClientID)
	if err != nil || state.ActiveSessions != 0 || state.Usage.RawUpload != 0 {
		t.Fatalf("expired handoff retained traffic/session: %+v/%v", state, err)
	}
}
