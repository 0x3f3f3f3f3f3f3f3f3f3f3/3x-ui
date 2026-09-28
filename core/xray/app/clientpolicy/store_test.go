package clientpolicy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func persistentEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.db")
	if err := CreateStore(path, "node-1"); err != nil {
		t.Fatal(err)
	}
	e, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, path
}

func TestPersistentGracefulRestartPreservesFractionVersionAndRevocation(t *testing.T) {
	e, path := persistentEngine(t)
	p := testPolicy("account")
	p.Multiplier = 500000
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 3); err != nil {
		t.Fatal(err)
	}
	p.Version++
	p.Multiplier = 1500000
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(Download, 3); err != nil {
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
	snap, err := restored.Snapshot(p.ClientID)
	if err != nil || snap.Usage != (Usage{RawUpload: 3, RawDownload: 3, BilledBytes: 6}) || snap.UncertainBytes != 0 || snap.PolicyVersion != 2 || snap.Epoch != 2 || snap.InstanceID != "node-1" || snap.Sequence == 0 {
		t.Fatalf("lost committed state: %+v %v", snap, err)
	}
	p.Version = 1
	if !errors.Is(restored.Apply(p), ErrPolicyVersion) {
		t.Fatal("accepted stale config after restart")
	}
	if err := restored.Remove(p.ClientID); err != nil {
		t.Fatal(err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	p.Version = 3
	if !errors.Is(third.Apply(p), ErrRevoked) {
		t.Fatal("reused revoked ID after restart")
	}
}

func TestPersistentAbruptExitRetainsBoundedReservationWithoutInventingRawUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.db")
	if err := CreateStore(path, "node-1"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPersistentCrashHelper$")
	cmd.Env = append(os.Environ(), "XRAY_POLICY_CRASH_PATH="+path)
	out, err := cmd.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 23 {
		t.Fatalf("helper did not reach crash boundary: %v %s", err, out)
	}
	e, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	snap, _ := e.Snapshot("crash")
	if snap.Usage != (Usage{RawUpload: reservationRawBytes, BilledBytes: 2 * reservationRawBytes}) || snap.UncertainBytes != 2*reservationRawBytes {
		t.Fatalf("reissued or fabricated crash usage: %+v", snap)
	}
	s := openSession(t, e, "crash", nil)
	if err := s.Admit(Download, reservationRawBytes); err != nil {
		t.Fatal(err)
	}
	snap, _ = e.Snapshot("crash")
	if snap.Reasons != ReasonQuota || snap.Usage.BilledBytes+snap.UncertainBytes != 6*reservationRawBytes {
		t.Fatalf("reserved budget reused: %+v", snap)
	}
	if _, err := e.Open(context.Background(), Metadata{ClientID: "crash"}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("quota reconnect: %v", err)
	}
}

func TestPersistentCrashHelper(t *testing.T) {
	path := os.Getenv("XRAY_POLICY_CRASH_PATH")
	if path == "" {
		return
	}
	e, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	p := testPolicy("crash")
	p.Multiplier, p.QuotaBytes = 2000000, 6*reservationRawBytes
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, "crash", nil)
	if err := s.Admit(Upload, reservationRawBytes); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(Download, 8); err != nil {
		t.Fatal(err)
	}
	os.Exit(23)
}

func TestPersistentMissingWrongOrOccupiedStoreFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := OpenPersistentEngine(path, "node-1"); err == nil {
		t.Fatal("recreated missing store")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing store created: %v", err)
	}
	if err := CreateStore(path, "node-1"); err != nil {
		t.Fatal(err)
	}
	if err := CreateStore(path, "node-1"); err == nil {
		t.Fatal("overwrote existing state")
	}
	if _, err := OpenPersistentEngine(path, "wrong-node"); err == nil {
		t.Fatal("accepted different store identity")
	}
	e, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := OpenPersistentEngine(path, "node-1"); err == nil {
		t.Fatal("two engines share one quota file")
	}
}

type failStore struct {
	stateStore
	afterCommit bool
}

func (f *failStore) save(r storedClient) (uint64, error) {
	if f.afterCommit {
		if _, err := f.stateStore.save(r); err != nil {
			return 0, err
		}
	}
	return 0, errors.New("injected durable commit failure")
}

func TestPersistentCommitFailureClosesAllClientsAndNeverAdmitsUnreservedBytes(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[after], func(t *testing.T) {
			e, path := persistentEngine(t)
			var closed atomic.Int32
			for _, id := range []string{"first", "second"} {
				if err := e.Apply(testPolicy(id)); err != nil {
					t.Fatal(err)
				}
			}
			s := openSession(t, e, "first", func() { closed.Add(1) })
			openSession(t, e, "second", func() { closed.Add(1) })
			e.store = &failStore{stateStore: e.store, afterCommit: after}
			if err := s.Admit(Upload, 1); !errors.Is(err, ErrStorage) {
				t.Fatalf("failed write admitted traffic: %v", err)
			}
			if closed.Load() != 2 {
				t.Fatalf("storage failure left %d connections open", 2-closed.Load())
			}
			snap, _ := e.Snapshot("first")
			if snap.Usage != (Usage{}) || snap.Reasons&ReasonStorage == 0 {
				t.Fatalf("failure not reflected: %+v", snap)
			}
			if err := e.Close(); err == nil {
				t.Fatal("lost persistence failure on close")
			}
			reopened, err := OpenPersistentEngine(path, "node-1")
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			snap, _ = reopened.Snapshot("first")
			want := uint64(0)
			if after {
				want = reservationRawBytes
			}
			if snap.Usage != (Usage{}) || snap.UncertainBytes != want {
				t.Fatalf("ambiguous commit recovery: %+v, want uncertain %d", snap, want)
			}
		})
	}
}

func TestPersistentSmallPayloadsUseBoundedAmortizedReservations(t *testing.T) {
	e, _ := persistentEngine(t)
	if err := e.Apply(testPolicy("small")); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Snapshot("small")
	s := openSession(t, e, "small", nil)
	for i := 0; i < 2*reservationRawBytes+1; i++ {
		if err := s.Admit(Upload, 1); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := e.Snapshot("small")
	if after.Sequence-before.Sequence != 3 {
		t.Fatalf("expected three reservation commits for 131073 one-byte admissions: %+v -> %+v", before, after)
	}
	if err := e.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := e.Snapshot("small")
	if checkpoint.Usage.RawUpload != 2*reservationRawBytes+1 || checkpoint.UncertainBytes != 0 {
		t.Fatalf("incorrect checkpoint: %+v", checkpoint)
	}
}

func TestPersistentCorruptStoreCannotBecomeAnEmptyAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("damaged state"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPersistentEngine(path, "node-1"); err == nil {
		t.Fatal("accepted corrupt state")
	}
}

func TestPersistentBatchRejectsWholeUpdateAndPreservesOldPolicies(t *testing.T) {
	e, path := persistentEngine(t)
	a, b := testPolicy("a"), testPolicy("b")
	b.Version = 2
	for _, p := range []Policy{a, b} {
		if err := e.Apply(p); err != nil {
			t.Fatal(err)
		}
	}
	a.Version = 2
	a.Multiplier = 2000000
	b.Version = 1
	if err := e.ApplyBatch([]Policy{a, b}); !errors.Is(err, ErrPolicyVersion) {
		t.Fatalf("invalid batch: %v", err)
	}
	snap, _ := e.Snapshot("a")
	if snap.PolicyVersion != 1 {
		t.Fatalf("partially applied rejected batch: %+v", snap)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	snap, _ = recovered.Snapshot("a")
	if snap.PolicyVersion != 1 {
		t.Fatalf("partially committed rejected batch: %+v", snap)
	}
	a.Version = 2
	b.Version = 3
	if err := recovered.ApplyBatch([]Policy{a, b}); err != nil {
		t.Fatal(err)
	}
	snap, _ = recovered.Snapshot("a")
	if snap.PolicyVersion != 2 {
		t.Fatalf("valid batch missing: %+v", snap)
	}
}
