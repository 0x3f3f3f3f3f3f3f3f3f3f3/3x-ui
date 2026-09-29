package clientpolicy

import (
	"context"
	"errors"
	"testing"
)

func TestInitializePreservesLegacyQuotaAndRetriesAcrossRestart(t *testing.T) {
	e, path := persistentEngine(t)
	p := testPolicy("legacy")
	p.QuotaBytes, p.Multiplier = 100, 2*MultiplierScale
	seed := Usage{RawUpload: 10, RawDownload: 20, BilledBytes: 30}
	if err := e.Initialize(p, seed); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 10); err != nil {
		t.Fatal(err)
	}
	if err := e.Initialize(p, seed); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Initialize(p, seed); err != nil {
		t.Fatal(err)
	}
	p.Version++
	p.Multiplier = MultiplierScale
	if err := reopened.Apply(p); err != nil {
		t.Fatal(err)
	}
	initial := p
	initial.Version, initial.Multiplier = 1, 2*MultiplierScale
	if err := reopened.Initialize(initial, seed); err != nil {
		t.Fatal(err)
	}
	s = openSession(t, reopened, p.ClientID, nil)
	if err := s.Admit(Download, 50); err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Snapshot(p.ClientID)
	if err != nil || snapshot.Usage != (Usage{RawUpload: 20, RawDownload: 70, BilledBytes: 100}) || snapshot.Reasons != ReasonQuota || snapshot.PolicyVersion != 2 {
		t.Fatalf("legacy quota was reissued or repriced: %+v %v", snapshot, err)
	}
	if _, err := reopened.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("quota reconnect: %v", err)
	}
	if err := reopened.Initialize(initial, Usage{}); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("seed overwritten: %v", err)
	}
}

func TestInitializeRejectsExistingAndInvalidSeed(t *testing.T) {
	e, _ := persistentEngine(t)
	p := testPolicy("existing")
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if err := e.Initialize(p, Usage{}); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("adopted an existing unseeded identity: %v", err)
	}
	p.ClientID = "invalid"
	if err := e.Initialize(p, Usage{Remainder: MultiplierScale}); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("invalid remainder: %v", err)
	}
	if _, err := e.Snapshot(p.ClientID); !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("failed initialization published identity: %v", err)
	}
	p.ClientID, p.QuotaBytes = "exhausted", 10
	if err := e.Initialize(p, Usage{RawDownload: 20, BilledBytes: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("exhausted legacy quota restored: %v", err)
	}
	if err := e.Remove(p.ClientID); err != nil {
		t.Fatal(err)
	}
	if err := e.Initialize(p, Usage{RawDownload: 20, BilledBytes: 20}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked identity resurrected: %v", err)
	}
}

func TestInitializeCommitFailureCannotPublishOrDuplicateSeed(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[after], func(t *testing.T) {
			e, path := persistentEngine(t)
			original := e.store
			e.store = &failStore{stateStore: original, afterCommit: after}
			p, seed := testPolicy("legacy"), Usage{RawUpload: 7, BilledBytes: 7}
			if err := e.Initialize(p, seed); !errors.Is(err, ErrStorage) {
				t.Fatalf("failure not surfaced: %v", err)
			}
			if e.Capabilities().Ready {
				t.Fatal("failed storage remains ready")
			}
			if _, err := e.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); err == nil {
				t.Fatal("failed initialization admits traffic")
			}
			_ = e.Close()
			reopened, err := OpenPersistentEngine(path, "node-1")
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			_, priorErr := reopened.Snapshot(p.ClientID)
			if after && priorErr != nil || !after && !errors.Is(priorErr, ErrUnknownClient) {
				t.Fatalf("wrong durable commit boundary: %v", priorErr)
			}
			if err := reopened.Initialize(p, seed); err != nil {
				t.Fatal(err)
			}
			snap, err := reopened.Snapshot(p.ClientID)
			if err != nil || snap.Usage != seed {
				t.Fatalf("seed duplicated after retry: %+v %v", snap, err)
			}
		})
	}
}
