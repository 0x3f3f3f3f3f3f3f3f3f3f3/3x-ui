package clientpolicy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuthorityDemandRefillsAnExhaustedGrantWithoutClosingLiveStream(t *testing.T) {
	p := testPolicy("refill-owner")
	p.Multiplier, p.QuotaBytes = 1500000, 100
	e, grant := authorityExecutionFixture(t, p, 15, 2*time.Second)
	if err := e.EnableAuthorityRequests(grant.BootID, grant.Authority); err != nil {
		t.Fatal(err)
	}
	if _, err := e.InstallAuthorityGrant(grant); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	defer s.Close()
	if err := s.Admit(Upload, 10); err != nil {
		t.Fatal(err)
	}
	before, err := e.Snapshot(p.ClientID)
	if err != nil || before.ActiveSessions != 1 {
		t.Fatalf("exhausted finite grant closed eligible stream: %+v/%v", before, err)
	}
	admitted := make(chan error, 1)
	go func() { admitted <- s.Admit(Download, 3) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	requests, err := e.WaitAuthorityRequests(ctx, grant.BootID, grant.Authority, 128)
	if err != nil || len(requests) != 1 || requests[0].PreviousGrantID != grant.GrantID {
		t.Fatalf("refill request: %+v/%v", requests, err)
	}
	sealed, err := e.PauseAuthorityGrant(p.ClientID, grant.GrantID)
	if err != nil || sealed.Usage != (Usage{RawUpload: 10, BilledBytes: 15}) {
		t.Fatalf("refill seal changed exact usage: %+v/%v", sealed, err)
	}
	select {
	case err := <-admitted:
		t.Fatalf("sealed stream admitted before replacement: %v", err)
	default:
	}
	challenge, err := e.BeginAuthorityChallenge(grant.BootID)
	if err != nil {
		t.Fatal(err)
	}
	next := grant
	next.GrantID, next.Sequence, next.ChallengeID, next.Capacity = "refill-grant-2", 2, challenge.ChallengeID, 20
	if _, err := e.InstallAuthorityGrant(next); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("live stream failed to resume after refill")
	}
	after, err := e.Snapshot(p.ClientID)
	if err != nil || after.ActiveSessions != 1 || after.Usage != (Usage{RawUpload: 10, RawDownload: 3, BilledBytes: 19, Remainder: 500000}) {
		t.Fatalf("refill duplicated multiplier or session: %+v/%v", after, err)
	}
}

func TestAuthorityDemandRefillCannotPassExpiryOrGlobalQuota(t *testing.T) {
	for _, quota := range []bool{false, true} {
		t.Run(map[bool]string{false: "lease-expiry", true: "global-quota"}[quota], func(t *testing.T) {
			p := testPolicy("refill-boundary")
			p.QuotaBytes = 100
			duration := 100 * time.Millisecond
			if quota {
				p.QuotaBytes = 10
				duration = time.Second
			}
			e, g := authorityExecutionFixture(t, p, 10, duration)
			if err := e.EnableAuthorityRequests(g.BootID, g.Authority); err != nil {
				t.Fatal(err)
			}
			if _, err := e.InstallAuthorityGrant(g); err != nil {
				t.Fatal(err)
			}
			s := openSession(t, e, p.ClientID, nil)
			defer s.Close()
			if err := s.Admit(Upload, 10); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if _, err := s.StreamChunkSize(Download); err == nil {
				t.Fatal("unfunded refill admitted")
			}
			if time.Since(start) > 500*time.Millisecond {
				t.Fatal("refill waited beyond its old monotonic lease")
			}
			if err := s.Admit(Download, 1); !errors.Is(err, ErrSessionClosed) {
				t.Fatalf("expired/quota session still admitted: %v", err)
			}
			state, err := e.Snapshot(p.ClientID)
			// A concurrent expiry can own Close's CAS while its map cleanup is
			// still finishing. Admission must already be closed above.
			for err == nil && state.ActiveSessions != 0 && time.Since(start) < 500*time.Millisecond {
				time.Sleep(time.Millisecond)
				state, err = e.Snapshot(p.ClientID)
			}
			if err != nil || state.Usage != (Usage{RawUpload: 10, BilledBytes: 10}) || state.ActiveSessions != 0 {
				t.Fatalf("refill boundary billed or retained a session: %+v/%v", state, err)
			}
			e.demandMu.Lock()
			defer e.demandMu.Unlock()
			if e.demandWaiters != 0 || len(e.demandRequests) != 0 {
				t.Fatal("failed refill retained demand capacity")
			}
		})
	}
}

func TestAuthorityDemandOversizedPacketDoesNotIssueRepeatedRefills(t *testing.T) {
	p := testPolicy("refill-oversized")
	p.QuotaBytes = 100
	e, g := authorityExecutionFixture(t, p, 10, time.Second)
	if err := e.EnableAuthorityRequests(g.BootID, g.Authority); err != nil {
		t.Fatal(err)
	}
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	defer s.Close()
	if err := s.Admit(Upload, 11); !errors.Is(err, ErrRestricted) {
		t.Fatalf("oversized packet refill result: %v", err)
	}
	state, err := e.Snapshot(p.ClientID)
	if err != nil || state.Usage != (Usage{}) {
		t.Fatalf("oversized packet billed: %+v/%v", state, err)
	}
	e.demandMu.Lock()
	defer e.demandMu.Unlock()
	if e.demandWaiters != 0 || len(e.demandRequests) != 0 {
		t.Fatal("oversized packet entered refill loop")
	}
}

func TestAuthorityDemandRefillPreservesSpentRateBurst(t *testing.T) {
	p := testPolicy("refill-rate")
	p.UploadRate, p.BurstBytes, p.QuotaBytes = 100, 10, 100
	e, g := authorityExecutionFixture(t, p, 2, time.Second)
	g.Upload = AuthorityShare{Rate: 10, Burst: 2}
	if err := e.EnableAuthorityRequests(g.BootID, g.Authority); err != nil {
		t.Fatal(err)
	}
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	defer s.Close()
	if err := s.Admit(Upload, 2); err != nil {
		t.Fatal(err)
	}
	admitted := make(chan error, 1)
	go func() { admitted <- s.Admit(Upload, 1) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := e.WaitAuthorityRequests(ctx, g.BootID, g.Authority, 128); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PauseAuthorityGrant(p.ClientID, g.GrantID); err != nil {
		t.Fatal(err)
	}
	challenge, err := e.BeginAuthorityChallenge(g.BootID)
	if err != nil {
		t.Fatal(err)
	}
	g.GrantID, g.Sequence, g.ChallengeID = "rate-refill-2", 2, challenge.ChallengeID
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-admitted:
		t.Fatalf("refill recreated spent burst: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("refilled rate limiter never admitted")
	}
	state, err := e.Snapshot(p.ClientID)
	if err != nil || state.Usage != (Usage{RawUpload: 3, BilledBytes: 3}) {
		t.Fatalf("rate refill usage: %+v/%v", state, err)
	}
}

func TestAuthorityDemandWaitsWithoutBillingUntilCurrentGrantIsInstalled(t *testing.T) {
	policy := testPolicy("demand-owner")
	policy.Multiplier, policy.QuotaBytes = 2000000, 100
	engine, grant := authorityExecutionFixture(t, policy, 40, 2*time.Second)
	if err := engine.EnableAuthorityRequests(grant.BootID, grant.Authority); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type result struct {
		session *Session
		err     error
	}
	opened := make(chan result, 1)
	go func() {
		session, err := engine.Open(ctx, Metadata{ClientID: policy.ClientID}, nil)
		opened <- result{session, err}
	}()
	requests, err := engine.WaitAuthorityRequests(ctx, grant.BootID, grant.Authority, 128)
	if err != nil || len(requests) != 1 || requests[0].ClientID != policy.ClientID || requests[0].PolicyVersion != 1 || len(requests[0].RequestID) != 32 || requests[0].PreviousGrantID != "" {
		t.Fatalf("wrong demand request: %+v/%v", requests, err)
	}
	select {
	case got := <-opened:
		if got.session != nil {
			got.session.Close()
		}
		t.Fatalf("demand admitted or rejected before its grant: %v", got.err)
	default:
	}
	snapshot, err := engine.Snapshot(policy.ClientID)
	if err != nil || snapshot.Usage != (Usage{}) || snapshot.ActiveSessions != 0 {
		t.Fatalf("waiting demand billed or opened a session: %+v/%v", snapshot, err)
	}
	if _, err := engine.InstallAuthorityGrant(grant); err != nil {
		t.Fatal(err)
	}
	got := <-opened
	if got.err != nil || got.session == nil {
		t.Fatalf("current grant did not wake demand: %v", got.err)
	}
	defer got.session.Close()
	if err := got.session.Admit(Upload, 3); err != nil {
		t.Fatal(err)
	}
	state, err := engine.GetAuthorityGrant(policy.ClientID)
	if err != nil || state.Usage != (Usage{RawUpload: 3, BilledBytes: 6}) {
		t.Fatalf("demand bypassed exact grant billing: %+v/%v", state, err)
	}
}

func TestAuthorityDemandCancellationRetainsRequestIdentityAndNoAllowance(t *testing.T) {
	policy := testPolicy("cancelled-demand")
	engine, grant := authorityExecutionFixture(t, policy, 40, time.Second)
	if err := engine.EnableAuthorityRequests(grant.BootID, grant.Authority); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	opened := make(chan error, 1)
	go func() {
		session, err := engine.Open(ctx, Metadata{ClientID: policy.ClientID}, nil)
		if session != nil {
			session.Close()
		}
		opened <- err
	}()
	pull, pullCancel := context.WithTimeout(context.Background(), time.Second)
	defer pullCancel()
	first, err := engine.WaitAuthorityRequests(pull, grant.BootID, grant.Authority, 128)
	if err != nil || len(first) != 1 {
		cancel()
		t.Fatalf("missing first request: %+v/%v", first, err)
	}
	cancel()
	if err := <-opened; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled demand admitted: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go func() {
		session, err := engine.Open(ctx, Metadata{ClientID: policy.ClientID}, nil)
		if session != nil {
			session.Close()
		}
		opened <- err
	}()
	second, err := engine.WaitAuthorityRequests(pull, grant.BootID, grant.Authority, 128)
	if err != nil || len(second) != 1 || second[0] != first[0] {
		t.Fatalf("retry changed issuance identity: %+v/%+v/%v", first, second, err)
	}
	cancel()
	if err := <-opened; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retry admitted: %v", err)
	}
	snapshot, err := engine.Snapshot(policy.ClientID)
	if err != nil || snapshot.Usage != (Usage{}) || snapshot.ActiveSessions != 0 || snapshot.Reasons&ReasonAuthority == 0 {
		t.Fatalf("cancelled requests fabricated allowance: %+v/%v", snapshot, err)
	}
}

func TestAuthorityDemandCannotEnableOrReadFromWrongBootOrBinding(t *testing.T) {
	engine, grant := authorityExecutionFixture(t, testPolicy("bound-demand"), 40, time.Second)
	if err := engine.EnableAuthorityRequests("copied-boot", grant.Authority); !errors.Is(err, ErrAuthority) {
		t.Fatalf("copied boot enabled demand: %v", err)
	}
	other := grant.Authority
	other.Generation++
	if err := engine.EnableAuthorityRequests(grant.BootID, other); !errors.Is(err, ErrAuthority) {
		t.Fatalf("wrong authority enabled demand: %v", err)
	}
	if err := engine.EnableAuthorityRequests(grant.BootID, grant.Authority); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.WaitAuthorityRequests(context.Background(), "copied-boot", grant.Authority, 128); !errors.Is(err, ErrAuthority) {
		t.Fatalf("copied boot read demand: %v", err)
	}
	if _, err := engine.WaitAuthorityRequests(context.Background(), grant.BootID, other, 128); !errors.Is(err, ErrAuthority) {
		t.Fatalf("wrong authority read demand: %v", err)
	}
}

func TestAuthorityDemandTimeoutNeverAdmitsOrBills(t *testing.T) {
	engine, grant := authorityExecutionFixture(t, testPolicy("unavailable-demand"), 40, time.Second)
	if err := engine.EnableAuthorityRequests(grant.BootID, grant.Authority); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	session, err := engine.Open(context.Background(), Metadata{ClientID: grant.ClientID}, nil)
	if session != nil {
		session.Close()
	}
	if !errors.Is(err, ErrAuthority) || time.Since(start) < maxAuthorityRequestWait || time.Since(start) > maxAuthorityRequestWait+time.Second {
		t.Fatalf("missing issuer did not fail within demand bound: %v/%s", err, time.Since(start))
	}
	snapshot, err := engine.Snapshot(grant.ClientID)
	if err != nil || snapshot.Usage != (Usage{}) || snapshot.ActiveSessions != 0 {
		t.Fatalf("timed-out request billed traffic: %+v/%v", snapshot, err)
	}
	engine.demandMu.Lock()
	defer engine.demandMu.Unlock()
	if engine.demandWaiters != 0 || len(engine.demandRequests) != 0 {
		t.Fatal("timed-out demand retained queue capacity")
	}
}

func TestAuthorityDemandBacklogBoundAndPolicyChangeCancelWaiters(t *testing.T) {
	engine, grant := authorityExecutionFixture(t, testPolicy("bounded-demand"), 40, time.Second)
	if err := engine.EnableAuthorityRequests(grant.BootID, grant.Authority); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan error, maxAuthorityRequestWaiters)
	for i := 0; i < maxAuthorityRequestWaiters; i++ {
		go func() {
			session, err := engine.Open(ctx, Metadata{ClientID: grant.ClientID}, nil)
			if session != nil {
				session.Close()
			}
			opened <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for {
		engine.demandMu.Lock()
		n := engine.demandWaiters
		engine.demandMu.Unlock()
		if n == maxAuthorityRequestWaiters {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backlog did not fill: %d", n)
		}
		time.Sleep(time.Millisecond)
	}
	if session, err := engine.Open(ctx, Metadata{ClientID: grant.ClientID}, nil); !errors.Is(err, ErrQueueFull) {
		if session != nil {
			session.Close()
		}
		t.Fatalf("overflowing demand queue admitted: %v", err)
	}
	policy := testPolicy(grant.ClientID)
	policy.Version = 2
	if err := engine.Apply(policy); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxAuthorityRequestWaiters; i++ {
		select {
		case err := <-opened:
			if !errors.Is(err, ErrPolicyVersion) {
				t.Fatalf("superseded demand waited or admitted: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("policy change did not wake demand backlog")
		}
	}
	engine.demandMu.Lock()
	defer engine.demandMu.Unlock()
	if engine.demandWaiters != 0 || len(engine.demandRequests) != 0 {
		t.Fatal("superseded demands retained queue capacity")
	}
}

func TestAuthorityDemandControlWaiterWakesOnStorageFailureOrShutdown(t *testing.T) {
	for _, action := range []string{"storage-failure", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			engine, grant := authorityExecutionFixture(t, testPolicy("failure-demand"), 40, time.Second)
			if err := engine.EnableAuthorityRequests(grant.BootID, grant.Authority); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := engine.WaitAuthorityRequests(ctx, grant.BootID, grant.Authority, 128); result <- err }()
			deadline := time.Now().Add(time.Second)
			for {
				engine.demandMu.Lock()
				readers := engine.demandReaders
				engine.demandMu.Unlock()
				if readers == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("control wait did not register")
				}
				time.Sleep(time.Millisecond)
			}
			want := ErrStorage
			if action == "shutdown" {
				want = ErrEngineClosed
				if err := engine.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				engine.storageFailed(ErrStorage)
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("%s did not wake control waiter: %v", action, err)
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatalf("%s left control waiter hanging", action)
			}
		})
	}
}

func TestAuthorityDemandControlReaderBacklogIsBounded(t *testing.T) {
	engine, grant := authorityExecutionFixture(t, testPolicy("reader-demand"), 40, time.Second)
	if err := engine.EnableAuthorityRequests(grant.BootID, grant.Authority); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, maxAuthorityRequestReaders)
	for i := 0; i < maxAuthorityRequestReaders; i++ {
		go func() {
			_, err := engine.WaitAuthorityRequests(ctx, grant.BootID, grant.Authority, 128)
			results <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for {
		engine.demandMu.Lock()
		n := engine.demandReaders
		engine.demandMu.Unlock()
		if n == maxAuthorityRequestReaders {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("control reader backlog did not fill: %d", n)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := engine.WaitAuthorityRequests(ctx, grant.BootID, grant.Authority, 128); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("control reader overflow accepted: %v", err)
	}
	cancel()
	for i := 0; i < maxAuthorityRequestReaders; i++ {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled reader remained: %v", err)
		}
	}
	engine.demandMu.Lock()
	defer engine.demandMu.Unlock()
	if engine.demandReaders != 0 {
		t.Fatal("cancelled control readers retained capacity")
	}
}
