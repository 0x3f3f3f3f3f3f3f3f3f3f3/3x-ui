package clientpolicy

import (
	"context"
	"errors"
	"testing"
	"time"
)

type usageFloorReconciler interface {
	ReconcileUsageFloor(string, string, Usage) error
}

func TestUsageReconciliationRetainsRecoveredGrantConsumption(t *testing.T) {
	p := testPolicy("retained")
	e, g := authorityExecutionFixture(t, p, 100, time.Second)
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SealAuthorityGrant(p.ClientID, g.GrantID); err != nil {
		t.Fatal(err)
	}
	path := e.store.(*boltStore).db.Path()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := openPersistentEngine(path, "authority-node", "fresh-recovery-boot")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	before := *r.clients[p.ClientID].previousGrant
	if err := r.ReconcileUsageFloor(r.bootID, p.ClientID, Usage{RawUpload: 10, RawDownload: 8, BilledBytes: 40}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	last, err := openPersistentEngine(path, "authority-node", "another-recovery-boot")
	if err != nil {
		t.Fatalf("usage floor corrupted the retained grant: %v", err)
	}
	defer last.Close()
	stored := last.clients[p.ClientID].previousGrant
	spent, err := grantUsage(last.clients[p.ClientID].usage, stored.StartUsage)
	if err != nil || spent != (Usage{RawUpload: 3, BilledBytes: 3}) || stored.Grant != before.Grant || stored.Sealed != before.Sealed || stored.ReservedBilledBytes != before.ReservedBilledBytes || stored.ReservedRemainder != before.ReservedRemainder {
		t.Fatalf("recovered grant changed: %+v/%+v/%v", stored, spent, err)
	}
}

func TestUsageReconciliationStorageFailureClosesEngineWithoutDeadlock(t *testing.T) {
	e := configuredAuthorityEngine(t)
	p := testPolicy("failed")
	if err := e.Initialize(p, Usage{BilledBytes: 10}); err != nil {
		t.Fatal(err)
	}
	e.store = &failStore{stateStore: e.store}
	done := make(chan error, 1)
	go func() { done <- e.ReconcileUsageFloor(e.bootID, p.ClientID, Usage{BilledBytes: 20}) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStorage) || e.Capabilities().Ready {
			t.Fatalf("storage failure remained ready: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("usage reconciliation deadlocked while closing failed storage")
	}
}

func TestSnapshotDoesNotCountSessionWhoseCancellationHasCompleted(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("closing")
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	// Keep transport cleanup pending after cancellation becomes observable.
	s.cleanupMu.Lock()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	<-s.ctx.Done()
	snapshot, err := e.Snapshot(p.ClientID)
	_, control, controlErr := e.GetClient(p.ClientID)
	connections, connectionsErr := e.Connections(p.ClientID)
	s.cleanupMu.Unlock()
	<-done
	if err != nil || controlErr != nil || connectionsErr != nil || snapshot.ActiveSessions != 0 || control.ActiveSessions != 0 || len(connections) != 0 {
		t.Fatalf("cancelled session reported active during cleanup: %+v/%+v/%+v/%v/%v/%v", snapshot, control, connections, err, controlErr, connectionsErr)
	}
}

func TestDormantUsageReconciliationPreservesKnownCountersAndCannotAuthorizePayload(t *testing.T) {
	e := configuredAuthorityEngine(t)
	p := testPolicy("restored")
	if err := e.Initialize(p, Usage{RawUpload: 12, RawDownload: 4, BilledBytes: 20, Remainder: 700000}); err != nil {
		t.Fatal(err)
	}
	r, ok := any(e).(usageFloorReconciler)
	if !ok {
		t.Fatal("core cannot reconcile retained known usage before authority binding")
	}
	boot := e.Capabilities().BootID
	floor := Usage{RawUpload: 10, RawDownload: 8, BilledBytes: 20, Remainder: 900000}
	if err := r.ReconcileUsageFloor(boot, p.ClientID, floor); err != nil {
		t.Fatal(err)
	}
	snap, err := e.Snapshot(p.ClientID)
	want := Usage{RawUpload: 12, RawDownload: 8, BilledBytes: 20, Remainder: 900000}
	if err != nil || snap.Usage != want || snap.PolicyVersion != 1 {
		t.Fatalf("reconciliation lowered or repriced existing usage: %+v/%v", snap, err)
	}
	if err := r.ReconcileUsageFloor(boot, p.ClientID, Usage{}); err != nil {
		t.Fatal(err)
	}
	if s, err := e.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("usage floor authorized payload: %v", err)
	}
	path := e.store.(*boltStore).db.Path()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openPersistentEngine(path, "authority-node", "different-test-boot")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snap, err = reopened.Snapshot(p.ClientID)
	if err != nil || snap.Usage != want {
		t.Fatalf("floor was not durable: %+v/%v", snap, err)
	}
}

func TestUsageReconciliationRejectsWrongBootInvalidFractionAndBoundAuthority(t *testing.T) {
	e := configuredAuthorityEngine(t)
	p := testPolicy("restored")
	if err := e.Initialize(p, Usage{BilledBytes: 10}); err != nil {
		t.Fatal(err)
	}
	r, ok := any(e).(usageFloorReconciler)
	if !ok {
		t.Fatal("usage reconciliation is unavailable")
	}
	boot := e.Capabilities().BootID
	if err := r.ReconcileUsageFloor("old-boot", p.ClientID, Usage{BilledBytes: 15}); !errors.Is(err, ErrAuthority) {
		t.Fatalf("old boot: %v", err)
	}
	if err := r.ReconcileUsageFloor(boot, p.ClientID, Usage{Remainder: MultiplierScale}); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("invalid fraction: %v", err)
	}
	if err := e.BindAuthority(boot, AuthorityBinding{AuthorityID: "issuer", Generation: 1, NodeID: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileUsageFloor(boot, p.ClientID, Usage{BilledBytes: 15}); !errors.Is(err, ErrAuthority) {
		t.Fatalf("bound engine accepted a baseline edit: %v", err)
	}
	snap, err := e.Snapshot(p.ClientID)
	if err != nil || snap.Usage.BilledBytes != 10 {
		t.Fatalf("rejected edit changed usage: %+v/%v", snap, err)
	}
}

func TestUsageReconciliationCannotReopenLostRelativeExpiryBoundary(t *testing.T) {
	e := configuredAuthorityEngine(t)
	p := testPolicy("relative-expiry")
	p.ExpiresAt = -60000
	if err := e.Initialize(p, Usage{}); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileUsageFloor(e.bootID, p.ClientID, Usage{RawUpload: 4, BilledBytes: 4}); !errors.Is(err, ErrAuthority) {
		t.Fatalf("lost first-use time silently reopened relative expiry: %v", err)
	}
	snap, err := e.Snapshot(p.ClientID)
	if err != nil || snap.Usage != (Usage{}) || snap.FirstUsedAt != 0 {
		t.Fatalf("rejected expiry recovery changed state: %+v/%v", snap, err)
	}
}

func TestUsageReconciliationPreservesKnownRelativeExpiryBoundary(t *testing.T) {
	legacy, path := persistentEngine(t)
	p := testPolicy("retained-relative-expiry")
	p.ExpiresAt = -60000
	if err := legacy.Initialize(p, Usage{}); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, legacy, p.ClientID, nil)
	if err := s.Admit(Upload, 1); err != nil {
		t.Fatal(err)
	}
	before, err := legacy.Snapshot(p.ClientID)
	if err != nil || before.FirstUsedAt <= 0 {
		t.Fatalf("actual first-use boundary missing: %+v/%v", before, err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := openPersistentEngine(path, "node-1", "fresh-relative-expiry-boot")
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if err := recovered.ReconcileUsageFloor(recovered.bootID, p.ClientID, Usage{RawUpload: 3, BilledBytes: 3}); err != nil {
		t.Fatal(err)
	}
	after, err := recovered.Snapshot(p.ClientID)
	if err != nil || after.FirstUsedAt != before.FirstUsedAt || after.Usage.BilledBytes != 3 {
		t.Fatalf("floor restarted known relative lifetime: %+v/%+v/%v", before, after, err)
	}
}
