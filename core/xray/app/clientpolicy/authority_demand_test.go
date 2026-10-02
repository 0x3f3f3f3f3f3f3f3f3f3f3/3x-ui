package clientpolicy

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
