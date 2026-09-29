package clientpolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

func quotaWindow(t *testing.T, p Policy, billed, remainder uint64) Policy {
	t.Helper()
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"quotaBaselineBytes":%d,"quotaBaselineRemainder":%d}`, billed, remainder)), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuotaWindowResetPreservesLifetimeFractionAndRestart(t *testing.T) {
	e, path := persistentEngine(t)
	p := testPolicy("window")
	p.Multiplier, p.QuotaBytes = 500000, 2
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 3); err != nil {
		t.Fatal(err)
	}
	p.Version++
	p.QuotaBytes = 1
	p = quotaWindow(t, p, 1, 500000)
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(Download, 2); err != nil {
		t.Fatalf("reset must admit exactly one fresh billed byte: %v", err)
	}
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	policy, snapshot, err := restored.GetClient(p.ClientID)
	if err != nil || policy != p || snapshot.Usage != (Usage{RawUpload: 3, RawDownload: 2, BilledBytes: 2, Remainder: 500000}) || snapshot.Reasons != ReasonQuota || snapshot.UncertainBytes != 0 {
		t.Fatalf("reset changed lifetime history or reissued quota: %+v %+v %v", policy, snapshot, err)
	}
	if _, err := restored.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("restarted exhausted window admitted traffic: %v", err)
	}
}

func TestQuotaWindowResetDoesNotClearOtherRestrictions(t *testing.T) {
	e, _ := persistentEngine(t)
	p := testPolicy("disabled-expired")
	p.Enabled, p.ExpiresAt, p.QuotaBytes = false, time.Now().Add(-time.Hour).UnixMilli(), 1
	seed := Usage{RawUpload: 21, BilledBytes: 10, Remainder: 500000}
	if err := e.Initialize(p, seed); err != nil {
		t.Fatal(err)
	}
	p.Version++
	p = quotaWindow(t, p, 10, 500000)
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	snapshot, err := e.Snapshot(p.ClientID)
	if err != nil || snapshot.Reasons != ReasonDisabled|ReasonExpired || snapshot.Usage != seed {
		t.Fatalf("reset must clear only quota: %+v %v", snapshot, err)
	}
	if err := e.Remove(p.ClientID); err != nil {
		t.Fatal(err)
	}
	p.Version++
	if err := e.Apply(p); !errors.Is(err, ErrRevoked) {
		t.Fatalf("reset resurrected revoked identity: %v", err)
	}
}

func TestQuotaWindowRejectsFutureBaselinesAtomically(t *testing.T) {
	e, _ := persistentEngine(t)
	p := testPolicy("existing")
	if err := e.Initialize(p, Usage{BilledBytes: 10, Remainder: 500000}); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]uint64{{11, 0}, {10, 500001}, {0, MultiplierScale}} {
		bad := quotaWindow(t, p, pair[0], pair[1])
		bad.Version++
		if err := e.ApplyBatch([]Policy{testPolicy("new"), bad}); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("accepted baseline ahead of usage %v: %v", pair, err)
		}
		if _, err := e.Snapshot("new"); !errors.Is(err, ErrUnknownClient) {
			t.Fatalf("invalid batch published other member: %v", err)
		}
		actual, _, err := e.GetClient(p.ClientID)
		if err != nil || actual != p {
			t.Fatalf("invalid batch changed existing policy: %+v %v", actual, err)
		}
	}
	bad := quotaWindow(t, testPolicy("unseeded"), 1, 0)
	if err := e.Apply(bad); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("new identity acquired future baseline: %v", err)
	}
	if err := e.Initialize(bad, Usage{}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("seed acquired future baseline: %v", err)
	}
}

func TestQuotaWindowDoesNotOverflowAbsoluteCeiling(t *testing.T) {
	e, _ := persistentEngine(t)
	p := quotaWindow(t, testPolicy("near-limit"), math.MaxUint64-2, 750000)
	p.QuotaBytes = 100
	if err := e.Initialize(p, Usage{BilledBytes: math.MaxUint64 - 2, Remainder: 750000}); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 1); err != nil {
		t.Fatalf("valid relative budget overflowed its absolute ceiling: %v", err)
	}
	snapshot, err := e.Snapshot(p.ClientID)
	if err != nil || snapshot.Usage.BilledBytes != math.MaxUint64-1 || snapshot.Usage.Remainder != 750000 || snapshot.Usage.RawUpload != 1 {
		t.Fatalf("large lifetime usage changed: %+v %v", snapshot, err)
	}
}

func TestQuotaWindowCommitFailureHasOneDurableBoundary(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-commit-%t", after), func(t *testing.T) {
			e, path := persistentEngine(t)
			p := testPolicy("window")
			p.Multiplier, p.QuotaBytes = 500000, 10
			seed := Usage{RawUpload: 3, BilledBytes: 1, Remainder: 500000}
			if err := e.Initialize(p, seed); err != nil {
				t.Fatal(err)
			}
			s := openSession(t, e, p.ClientID, nil)
			if err := s.Admit(Upload, 1); err != nil {
				t.Fatal(err)
			}
			p.Version, p.QuotaBytes = 2, 2
			p = quotaWindow(t, p, 2, 0)
			e.store = &failQuotaBatchStore{stateStore: e.store, afterCommit: after}
			if err := e.Apply(p); !errors.Is(err, ErrStorage) {
				t.Fatalf("reset commit failure: %v", err)
			}
			if e.Capabilities().Ready {
				t.Fatal("failed reset left admission ready")
			}
			_ = e.Close()
			restored, err := OpenPersistentEngine(path, "node-1")
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			_, snapshot, err := restored.GetClient(p.ClientID)
			if err != nil {
				t.Fatal(err)
			}
			if after {
				if snapshot.PolicyVersion != 2 || snapshot.Usage != (Usage{RawUpload: 4, BilledBytes: 2}) || snapshot.UncertainBytes != 0 || snapshot.Reasons != 0 {
					t.Fatalf("committed reset lost exact boundary: %+v", snapshot)
				}
				if err := restored.Apply(p); err != nil {
					t.Fatal(err)
				}
				s = openSession(t, restored, p.ClientID, nil)
				if err := s.Admit(Download, 4); err != nil {
					t.Fatal(err)
				}
				snapshot, _ = restored.Snapshot(p.ClientID)
				if snapshot.Usage != (Usage{RawUpload: 4, RawDownload: 4, BilledBytes: 4}) || snapshot.Reasons != ReasonQuota {
					t.Fatalf("reset retry reissued budget: %+v", snapshot)
				}
			} else {
				if snapshot.PolicyVersion != 1 || snapshot.Usage != seed || snapshot.UncertainBytes != 9 || snapshot.Reasons != ReasonQuota {
					t.Fatalf("failed reset erased frozen usage: %+v", snapshot)
				}
				p = quotaWindow(t, p, 10, 500000)
				if err := restored.Apply(p); err != nil {
					t.Fatal(err)
				}
				s = openSession(t, restored, p.ClientID, nil)
				if err := s.Admit(Download, 4); err != nil {
					t.Fatal(err)
				}
				snapshot, _ = restored.Snapshot(p.ClientID)
				if snapshot.Usage != (Usage{RawUpload: 3, RawDownload: 4, BilledBytes: 3, Remainder: 500000}) || snapshot.UncertainBytes != 9 || snapshot.Reasons != ReasonQuota {
					t.Fatalf("explicit reset changed lifetime uncertainty: %+v", snapshot)
				}
			}
		})
	}
}

type failQuotaBatchStore struct {
	stateStore
	afterCommit bool
}

func (f *failQuotaBatchStore) saveBatch(records []storedClient) ([]uint64, error) {
	if f.afterCommit {
		if _, err := f.stateStore.saveBatch(records); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("injected reset batch commit failure")
}

func TestQuotaWindowLegacyInitializationDigestStillRetries(t *testing.T) {
	e, path := persistentEngine(t)
	legacy := []byte(`{"Policy":{"clientId":"legacy","version":1,"enabled":true,"multiplierMicros":1000000,"quotaBytes":0,"uploadBytesPerSecond":0,"downloadBytesPerSecond":0,"burstBytes":65536,"expiresAt":0},"Usage":{"rawUpload":3,"rawDownload":0,"billedBytes":1,"remainder":500000}}`)
	var record struct {
		Policy Policy
		Usage  Usage
	}
	if err := json.Unmarshal(legacy, &record); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(legacy)
	if _, err := e.store.save(storedClient{Policy: record.Policy, Usage: record.Usage, InitializationHash: hex.EncodeToString(digest[:])}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.Initialize(record.Policy, record.Usage); err != nil {
		t.Fatalf("new baseline fields broke the pre-existing initialization digest: %v", err)
	}
	s := openSession(t, restored, "legacy", nil)
	if err := s.Admit(Download, 2); err != nil {
		t.Fatal(err)
	}
	if err := restored.Initialize(record.Policy, record.Usage); err != nil {
		t.Fatal(err)
	}
	snapshot, err := restored.Snapshot("legacy")
	if err != nil || snapshot.Usage != (Usage{RawUpload: 3, RawDownload: 2, BilledBytes: 3, Remainder: 500000}) {
		t.Fatalf("legacy retry replaced newer lifetime usage: %+v %v", snapshot, err)
	}
}

func TestQuotaWindowCorruptFutureBaselineFailsRecovery(t *testing.T) {
	e, path := persistentEngine(t)
	p := quotaWindow(t, testPolicy("future"), 1, 500001)
	if _, err := e.store.save(storedClient{Policy: p, Usage: Usage{BilledBytes: 1, Remainder: 500000}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if restored, err := OpenPersistentEngine(path, "node-1"); !errors.Is(err, ErrStorage) {
		if restored != nil {
			restored.Close()
		}
		t.Fatalf("recovered a baseline ahead of durable usage: %v", err)
	}
}
