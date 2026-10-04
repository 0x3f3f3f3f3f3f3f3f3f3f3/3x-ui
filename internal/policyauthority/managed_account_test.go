package policyauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestManagedAccountFencesFourActualOlderWriters(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(strconv.FormatBool(populated), func(t *testing.T) {
			j, path := managedJournalFixture(t, nil, `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`)
			if err := activateManagedJournal(t, j, "managed-source"); err != nil {
				t.Fatal(err)
			}
			if populated {
				seed, origin := managedOriginFixture()
				if err := j.AddManagedAccount(seed, origin); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"RESET_OPERATION_OLD_WRITER_PROBE", "RESET_OPERATION_SCHEMA5_WRITER_PROBE", "CLIENT_MAPPING_SCHEMA6_WRITER_PROBE", "MANAGED_ACCOUNT_SCHEMA7_WRITER_PROBE"} {
				probe := mappingOldWriterProbe(t, name)
				result, err := exec.Command(probe, path, j.Identity().AuthorityID, strconv.FormatUint(j.Identity().Generation, 10)).CombinedOutput()
				var failure *exec.ExitError
				if !errors.As(err, &failure) || failure.ExitCode() != 3 || !strings.Contains(string(result), "REJECTED_JOURNAL") {
					t.Fatalf("older writer accepted managed origin: %s/%v", result, err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("older writer mutated managed original bytes")
				}
			}
		})
	}
}

func TestManagedAccountPreservesOriginalResetFloors(t *testing.T) {
	for _, floor := range []int{4, 5, 6} {
		t.Run(strconv.Itoa(floor), func(t *testing.T) {
			j, path := managedJournalFixture(t, nil, `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`)
			capture := ResetOperationCapture{Identity: j.Identity(), SourceID: "managed-source", RequestID: "retained-reset", Snapshot: `{"members":[]}`}
			prepared := ResetOperationPreparation{Identity: j.Identity(), SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: resetTestDigest(capture.Snapshot), Snapshot: `{"version":1}`}
			completion := ResetOperationCompletion{Identity: j.Identity(), SourceID: capture.SourceID, RequestID: capture.RequestID, PreparationDigest: resetTestDigest(prepared.Snapshot)}
			if floor >= 5 {
				if err := j.CaptureResetOperation(capture); err != nil {
					t.Fatal(err)
				}
			}
			if floor == 6 {
				if err := j.PrepareResetOperation(prepared); err != nil {
					t.Fatal(err)
				}
				if err := j.CompleteResetOperation(completion); err != nil {
					t.Fatal(err)
				}
			}
			if err := activateManagedJournal(t, j, "managed-source"); err != nil {
				t.Fatal(err)
			}
			if err := j.CaptureResetOperation(capture); err != nil {
				t.Fatal(err)
			}
			if err := j.PrepareResetOperation(prepared); err != nil {
				t.Fatal(err)
			}
			if err := j.CompleteResetOperation(completion); err != nil {
				t.Fatal(err)
			}
			if err := j.db.View(func(tx *bolt.Tx) error {
				var meta metadata
				if err := get(tx, "metadata", "state", &meta); err != nil {
					return err
				}
				if meta.Schema != 8 || meta.MappingBaseSchema != 6 {
					t.Fatalf("reset downgraded managed fence: %+v", meta)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(path, j.Identity())
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			retained, err := opened.LookupResetCompletion(capture.RequestID)
			if err != nil || retained != completion {
				t.Fatalf("original reset completion changed: %+v/%v", retained, err)
			}
		})
	}
}

func TestManagedAccountIssuanceRequiresOriginalEnrollmentAndVersion(t *testing.T) {
	j, path := managedJournalFixture(t, nil, `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`)
	if err := activateManagedJournal(t, j, "managed-source"); err != nil {
		t.Fatal(err)
	}
	seed, origin := managedOriginFixture()
	if err := j.AddManagedAccount(seed, origin); err != nil {
		t.Fatal(err)
	}
	boot := NodeBoot{"node-a", "source-a", "boot-a"}
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	request := Request{Binding: Binding{j.Identity(), boot, seed.ClientID, seed.Policy.WindowID, seed.Policy.Version}, RequestID: "managed-grant", ChallengeID: "managed-challenge", Capacity: 40, Upload: seed.Policy.Upload, Download: seed.Policy.Download, LeaseDuration: time.Second}
	if _, err := j.Issue(request); err == nil {
		t.Fatal("unmapped node minted a managed grant")
	}
	m := ClientMapping{Authority: j.Identity(), NodeAnchor: Identity{"node-original", 1}, NodeID: boot.NodeID, SourceID: boot.SourceID, GlobalClientID: seed.ClientID, LocalClientID: "22222222-2222-4222-8222-222222222222", GlobalPolicyVersion: seed.Policy.Version, LocalPolicyVersion: 1, PolicyDigest: strings.Repeat("b", 64)}
	if err := j.RecordClientMapping(ClientMappingCoordinator, m); err == nil {
		t.Fatal("enrollment discarded original full-policy digest")
	}
	m.PolicyDigest = origin.PolicyDigest
	if err := j.RecordClientMapping(ClientMappingCoordinator, m); err != nil {
		t.Fatal(err)
	}
	grant, err := j.Issue(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.CheckActiveGrant(grant.GrantID, boot); err != nil {
		t.Fatal(err)
	}
	next := seed.Policy
	next.Version++
	if _, err := j.ChangePolicy(ChangeRequest{Identity: j.Identity(), ClientID: seed.ClientID, RequestID: "unproven-version", ExpectedVersion: seed.Policy.Version, Policy: next}); err != nil {
		t.Fatal(err)
	}
	request.Binding.PolicyVersion, request.RequestID = next.Version, "after-unproven-version"
	if _, err := j.Issue(request); err == nil {
		t.Fatal("generic version change bypassed append-only policy proof")
	}
	if err := j.CheckActiveGrant(grant.GrantID, boot); err == nil {
		t.Fatal("unproven version renewed an old grant")
	}
	if err := j.Report(Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Usage: Usage{RawUpload: 3, BilledBytes: 4, Remainder: 500000}, Seal: true}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, j.Identity())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	a, err := reopened.Account(seed.ClientID)
	if err != nil || a.Usage.BilledBytes != 4 || a.Usage.Remainder != 500000 || a.HeldCapacity != 0 {
		t.Fatalf("original settlement lost: %+v/%v", a, err)
	}
}

func TestManagedNodeOriginsRefuseReplacementAndConsumedSeeds(t *testing.T) {
	j, _ := managedJournalFixture(t, nil, `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`)
	if err := activateManagedJournal(t, j, "managed-source"); err != nil {
		t.Fatal(err)
	}
	seed, origin := managedOriginFixture()
	origin.ParentClientID = "33333333-3333-4333-8333-333333333333"
	origin.Scope, origin.NodeID, origin.SourceID = "node", "node-a", "source-a"
	if err := j.AddManagedAccount(seed, origin); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"node", "source"} {
		other, changed := origin, seed
		other.ClientID, changed.ClientID = "22222222-2222-4222-8222-222222222222", "22222222-2222-4222-8222-222222222222"
		if keep == "node" {
			other.SourceID = "source-b"
		} else {
			other.NodeID = "node-b"
		}
		if err := j.AddManagedAccount(changed, other); err == nil {
			t.Fatalf("replacement %s minted an account", keep)
		}
		if _, err := j.LookupManagedNodeOrigin(other.ParentClientID, other.NodeID, other.SourceID); err == nil {
			t.Fatal("original source lookup accepted replacement identity")
		}
	}
	retained, err := j.LookupManagedNodeOrigin(origin.ParentClientID, origin.NodeID, origin.SourceID)
	if err != nil || retained != origin {
		t.Fatalf("original node identity is unavailable for SQL reconstruction: %+v/%v", retained, err)
	}
	if _, err := j.LookupManagedNodeOrigin(origin.ParentClientID, "unregistered-node", "unregistered-source"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing original account synthesized: %v", err)
	}
	consumed, other := seed, origin
	consumed.ClientID, other.ClientID = "44444444-4444-4444-8444-444444444444", "44444444-4444-4444-8444-444444444444"
	other.NodeID, other.SourceID = "node-b", "source-b"
	consumed.Usage.Remainder = 1
	if err := j.AddManagedAccount(consumed, other); err == nil {
		t.Fatal("consumed local source bypassed sealed handoff")
	}
}

func managedOriginFixture() (Seed, ManagedAccountOrigin) {
	client := "11111111-1111-4111-8111-111111111111"
	seed := Seed{ClientID: client, Policy: Policy{Version: 7, WindowID: "initial:" + client, QuotaBytes: 100, Upload: Direction{Unlimited: true}, Download: Direction{Unlimited: true}}}
	return seed, ManagedAccountOrigin{ClientID: client, ParentClientID: client, Scope: "global", InitialPolicyVersion: 7, PolicyDigest: strings.Repeat("a", 64)}
}

func TestManagedAccountOriginRetainsIdentityAndRejectsReseeding(t *testing.T) {
	j, path := managedJournalFixture(t, nil, `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`)
	if err := activateManagedJournal(t, j, "managed-source"); err != nil {
		t.Fatal(err)
	}
	seed, origin := managedOriginFixture()
	if err := j.AddAccount(seed); err == nil {
		t.Fatal("managed account bypassed its original origin")
	}
	if err := j.AddManagedAccount(seed, origin); err != nil {
		t.Fatal(err)
	}
	if err := j.AddManagedAccount(seed, origin); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	for _, field := range []string{"parent", "scope", "source", "node", "digest", "version", "seed"} {
		bad, changed := origin, seed
		switch field {
		case "parent":
			bad.ParentClientID = "22222222-2222-4222-8222-222222222222"
		case "scope":
			bad.Scope = "node"
		case "source":
			bad.SourceID = "new-source"
		case "node":
			bad.NodeID = "new-node"
		case "digest":
			bad.PolicyDigest = strings.Repeat("b", 64)
		case "version":
			bad.InitialPolicyVersion++
			changed.Policy.Version++
		case "seed":
			changed.Policy.QuotaBytes++
		}
		if err := j.AddManagedAccount(changed, bad); err == nil {
			t.Fatalf("retargeted %s", field)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, j.Identity())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ManagedAccountOrigin(seed.ClientID)
	if err != nil || got != origin {
		t.Fatalf("original origin changed: %+v/%v", got, err)
	}
	if err := reopened.Tombstone(seed.ClientID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.AddManagedAccount(seed, origin); !errors.Is(err, ErrDeleted) {
		t.Fatalf("tombstone reseeded: %v", err)
	}
}

func TestManagedAccountOriginRejectsCorruption(t *testing.T) {
	j, path := managedJournalFixture(t, nil, `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`)
	if err := activateManagedJournal(t, j, "managed-source"); err != nil {
		t.Fatal(err)
	}
	seed, origin := managedOriginFixture()
	if err := j.AddManagedAccount(seed, origin); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing-origin", "unknown-index", "scope", "version", "unknown-field", "duplicate-field", "downgrade"} {
		t.Run(fault, func(t *testing.T) {
			copyPath := filepath.Join(filepath.Dir(path), fault+".db")
			if err := os.WriteFile(copyPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(copyPath, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				b := tx.Bucket([]byte(managedAccountBucket))
				primary := "a/" + seed.ClientID
				switch fault {
				case "missing-origin":
					return b.Delete([]byte(primary))
				case "unknown-index":
					return b.Put([]byte("unknown"), []byte("foreign"))
				case "unknown-field":
					return b.Put([]byte(primary), append([]byte(`{"unknown":true,`), bytes.TrimPrefix(b.Get([]byte(primary)), []byte("{"))...))
				case "duplicate-field":
					return b.Put([]byte(primary), bytes.Replace(b.Get([]byte(primary)), []byte(`"scope":"global"`), []byte(`"scope":"node","scope":"global"`), 1))
				case "downgrade":
					var meta metadata
					if err := get(tx, "metadata", "state", &meta); err != nil {
						return err
					}
					meta.Schema, meta.MappingBaseSchema = 4, 0
					return put(tx, "metadata", "state", meta)
				default:
					bad := origin
					if fault == "scope" {
						bad.Scope = "node"
					} else {
						bad.InitialPolicyVersion++
					}
					raw, _ := json.Marshal(bad)
					return b.Put([]byte(primary), raw)
				}
			})
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if opened, err := Open(copyPath, j.Identity()); err == nil {
				_ = opened.Close()
				t.Fatal("corrupt original account reopened")
			}
		})
	}
}

func activateManagedJournal(t *testing.T, j *Journal, source string) error {
	t.Helper()
	api, ok := any(j).(interface{ ActivateManagedCoordinator(string) error })
	if !ok {
		t.Fatal("original journal cannot fence an empty managed coordinator before enrollment")
	}
	return api.ActivateManagedCoordinator(source)
}

func managedJournalFixture(t *testing.T, seeds []Seed, marker, role string) (*Journal, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "managed-original")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.db")
	records := []MigrationRecord{
		{Kind: "sources", Key: "managed-source", Value: []byte(marker)},
		{Kind: "execution-role", Key: "managed-source", Value: []byte(role)},
	}
	j, err := CreateWithMigrationSourceIdentity(path, Identity{"managed-original", 1}, "managed-source", seeds, nil, records)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, path
}

func TestManagedCoordinatorActivationRetainsEmptyOriginalFence(t *testing.T) {
	j, path := managedJournalFixture(t, nil, `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`)
	if err := activateManagedJournal(t, j, "managed-source"); err != nil {
		t.Fatal(err)
	}
	if err := activateManagedJournal(t, j, "managed-source"); err != nil {
		t.Fatalf("activation retry: %v", err)
	}
	if err := activateManagedJournal(t, j, "replacement-source"); !errors.Is(err, ErrIdentity) {
		t.Fatalf("replacement source: %v", err)
	}
	if err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		if meta.Schema != 8 || resetJournalSchema(meta) != 4 {
			t.Fatalf("missing managed fence: %+v", meta)
		}
		b := tx.Bucket([]byte(clientMappingBucket))
		if b == nil || b.Sequence() != 0 || b.Stats().KeyN != 0 {
			t.Fatal("activation fabricated mapping evidence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, j.Identity())
	if err != nil {
		t.Fatalf("empty managed journal did not reopen: %v", err)
	}
	defer reopened.Close()
	if err := activateManagedJournal(t, reopened, "managed-source"); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCoordinatorActivationRefusesOrdinaryOrPopulatedJournal(t *testing.T) {
	for _, tc := range []struct {
		name, marker, role string
		populated          bool
	}{
		{"ordinary", `{"role":"ordinary"}`, `{"mode":"local"}`, false},
		{"delegated", `{"role":"managed-coordinator","schema":1}`, `{"mode":"delegated"}`, false},
		{"populated", `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seeds []Seed
			if tc.populated {
				seeds = []Seed{{ClientID: "existing", Policy: Policy{Version: 1, WindowID: "initial", Upload: Direction{Unlimited: true}, Download: Direction{Unlimited: true}}}}
			}
			j, _ := managedJournalFixture(t, seeds, tc.marker, tc.role)
			if err := activateManagedJournal(t, j, "managed-source"); err == nil {
				t.Fatal("unrelated or populated journal became managed")
			}
		})
	}
}
