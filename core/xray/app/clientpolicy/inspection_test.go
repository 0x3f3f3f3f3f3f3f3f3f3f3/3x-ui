package clientpolicy

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	bolt "go.etcd.io/bbolt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func policyStoreDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func TestOfflineInspectionRetainsAuthorityOwnershipAfterSeal(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsealed", true: "sealed"}[sealed], func(t *testing.T) {
			e, grant := authorityExecutionFixture(t, testPolicy("owned-history"), 20, time.Second)
			path := e.store.(*boltStore).db.Path()
			if _, err := e.InstallAuthorityGrant(grant); err != nil {
				t.Fatal(err)
			}
			if sealed {
				if _, err := e.SealAuthorityGrant(grant.ClientID, grant.GrantID); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			snapshot, err := InspectPolicyStore(path, grant.InstanceID)
			if err != nil || len(snapshot.Clients) != 1 || !snapshot.Clients[0].HasAuthority {
				t.Fatalf("inspection lost prior authority ownership: %+v/%v", snapshot, err)
			}
		})
	}
}

func TestOfflineInspectionRejectsContradictorySequenceWithoutMutatingEvidence(t *testing.T) {
	e, path := persistentEngine(t)
	if err := e.ApplyBatch([]Policy{testPolicy("first"), testPolicy("second")}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0600, storeOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		clients := tx.Bucket(clientsBucket)
		var first, second storedClient
		if err := json.Unmarshal(clients.Get([]byte("first")), &first); err != nil {
			return err
		}
		if err := json.Unmarshal(clients.Get([]byte("second")), &second); err != nil {
			return err
		}
		second.Sequence = first.Sequence
		raw, err := json.Marshal(second)
		if err != nil {
			return err
		}
		return clients.Put([]byte("second"), raw)
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := policyStoreDigest(t, path)
	if snapshot, err := InspectPolicyStore(path, "node-1"); !errors.Is(err, ErrStorage) || snapshot.Clients != nil {
		t.Fatalf("contradictory sequence exposed a migration snapshot: %+v/%v", snapshot, err)
	}
	if policyStoreDigest(t, path) != before {
		t.Fatal("inspection changed contradictory evidence")
	}
}

func TestOfflineInspectionPreservesExactCountersEpochAndStoreBytes(t *testing.T) {
	e, path := persistentEngine(t)
	p := testPolicy("inspection")
	p.Multiplier = 1500000
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(Download, 2); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	before := policyStoreDigest(t, path)
	for range 2 {
		snapshot, err := InspectPolicyStore(path, "node-1")
		if err != nil || snapshot.InstanceID != "node-1" || snapshot.Epoch != 1 || snapshot.Sequence == 0 || len(snapshot.Clients) != 1 {
			t.Fatalf("offline inspection: %+v/%v", snapshot, err)
		}
		c := snapshot.Clients[0]
		if c.Policy != p || c.Usage != (Usage{RawUpload: 3, RawDownload: 2, BilledBytes: 7, Remainder: 500000}) || c.ReservedBytes != 0 || c.UncertainBytes != 0 || c.Epoch != 1 || c.Sequence != snapshot.Sequence || c.HasAuthority || c.Revoked {
			t.Fatalf("inspection changed charged history: %+v", c)
		}
		if policyStoreDigest(t, path) != before {
			t.Fatal("read-only inspection mutated execution state")
		}
	}
}

func TestOfflineInspectionRetainsAbruptReservationWithoutRecoveringOrInventingRawUsage(t *testing.T) {
	dir, err := os.MkdirTemp("", "policy-offline-crash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained abrupt execution inspection fixture: %s", dir)
	path := filepath.Join(dir, "state.db")
	if err := CreateStore(path, "node-1"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPersistentCrashHelper$")
	cmd.Env = append(os.Environ(), "XRAY_POLICY_CRASH_PATH="+path)
	out, err := cmd.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 23 {
		t.Fatalf("helper missed reservation crash: %v/%s", err, out)
	}
	before := policyStoreDigest(t, path)
	snapshot, err := InspectPolicyStore(path, "node-1")
	if err != nil || len(snapshot.Clients) != 1 {
		t.Fatalf("crashed execution inspection: %+v/%v", snapshot, err)
	}
	c := snapshot.Clients[0]
	if c.Epoch != 1 || c.Usage != (Usage{RawUpload: reservationRawBytes, BilledBytes: 2 * reservationRawBytes}) || c.ReservedBytes != 2*reservationRawBytes || c.UncertainBytes != 0 {
		t.Fatalf("inspection recovered or credited unresolved reservation: %+v", c)
	}
	if policyStoreDigest(t, path) != before {
		t.Fatal("inspection changed crash evidence")
	}
}

func TestOfflineInspectionRefusesLiveMissingUnsafeOrWrongIdentityStore(t *testing.T) {
	e, path := persistentEngine(t)
	if _, err := InspectPolicyStore(path, "node-1"); !errors.Is(err, ErrStorage) {
		t.Fatalf("inspection acquired a running execution file: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	before := policyStoreDigest(t, path)
	if _, err := InspectPolicyStore(path, "wrong-node"); !errors.Is(err, ErrStorage) {
		t.Fatalf("inspection accepted different source identity: %v", err)
	}
	missing := filepath.Join(filepath.Dir(path), "missing.db")
	if _, err := InspectPolicyStore(missing, "node-1"); !errors.Is(err, ErrStorage) {
		t.Fatalf("inspection recreated missing execution store: %v", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-state inspection left a file: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPolicyStore(path, "node-1"); !errors.Is(err, ErrStorage) {
		t.Fatalf("inspection accepted shared state permissions: %v", err)
	}
	if policyStoreDigest(t, path) != before {
		t.Fatal("failed inspection changed retained state")
	}
}
