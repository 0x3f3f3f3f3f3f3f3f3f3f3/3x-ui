package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func durableAuthorityFixture(t *testing.T) (string, authorityMigrationSnapshot) {
	t.Helper()
	dir, err := os.MkdirTemp("", "durable-authority-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained private authority state fixture: %s", dir)
	seed := policyauthority.Seed{ClientID: uuid.NewString(), Policy: policyauthority.Policy{WindowID: "initial", Version: 1, QuotaBytes: 100, Upload: policyauthority.Direction{Unlimited: true}, Download: policyauthority.Direction{Unlimited: true}}, Usage: policyauthority.Usage{BilledBytes: 10}, WindowUsed: 10}
	return dir, authorityMigrationSnapshot{Seeds: []policyauthority.Seed{seed}, Records: []policyauthority.MigrationRecord{{Kind: "totals", Key: seed.ClientID, Value: json.RawMessage(`{"billedBytes":10}`)}}}
}

func TestDurableAuthorityInitializationAndLostReplyReuseIdentityAndBudget(t *testing.T) {
	dir, snapshot := durableAuthorityFixture(t)
	state, err := initializeAuthorityState(dir, "local-source", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	id := state.Journal.Identity()
	boot := policyauthority.NodeBoot{NodeID: "local", SourceID: "local-source", BootID: "current-boot"}
	if err := state.Journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	seed := snapshot.Seeds[0]
	r := policyauthority.Request{Binding: policyauthority.Binding{Identity: id, NodeBoot: boot, ClientID: seed.ClientID, WindowID: seed.Policy.WindowID, PolicyVersion: 1}, RequestID: "allocated", ChallengeID: "challenge", Capacity: 60, Upload: seed.Policy.Upload, Download: seed.Policy.Download, LeaseDuration: time.Second}
	if _, err := state.Journal.Issue(r); err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = initializeAuthorityState(dir, "local-source", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if state.SourceID != "local-source" || state.Journal.Identity() != id {
		_ = state.Journal.Close()
		t.Fatalf("migration retry replaced identity/source: %+v", state)
	}
	a, err := state.Journal.Account(seed.ClientID)
	if err != nil || a.HeldCapacity != 60 || a.Revision != 2 {
		_ = state.Journal.Close()
		t.Fatalf("migration retry recreated allocation: %+v/%v", a, err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := openAuthorityState(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Journal.Close()
	if opened.Journal.Identity() != id {
		t.Fatal("ordinary open replaced durable identity")
	}
}

func TestDurableAuthorityOrdinaryOpenNeverInitializesMissingState(t *testing.T) {
	dir, snapshot := durableAuthorityFixture(t)
	missing := filepath.Join(dir, "not-initialized")
	if _, err := openAuthorityState(missing); !errors.Is(err, ErrAuthorityNotInitialized) {
		t.Fatalf("ordinary open accepted missing state: %v", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary open created authority directory: %v", err)
	}
	state, err := initializeAuthorityState(dir, "local-source", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "journal.db"), filepath.Join(dir, "retained-journal.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := openAuthorityState(dir); !errors.Is(err, ErrAuthorityNotInitialized) {
		t.Fatalf("ordinary open accepted missing activated journal: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "journal.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary open recreated journal: %v", err)
	}
}

func TestDurableAuthorityInitializationRejectsChangedSnapshotOrSource(t *testing.T) {
	dir, snapshot := durableAuthorityFixture(t)
	state, err := initializeAuthorityState(dir, "local-source", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if state, err := initializeAuthorityState(dir, "another-source", snapshot); !errors.Is(err, policyauthority.ErrIdentity) {
		if state != nil {
			_ = state.Journal.Close()
		}
		t.Fatalf("migration replaced source binding: %v", err)
	}
	snapshot.Seeds[0].WindowUsed--
	if state, err := initializeAuthorityState(dir, "local-source", snapshot); !errors.Is(err, policyauthority.ErrIdentity) {
		if state != nil {
			_ = state.Journal.Close()
		}
		t.Fatalf("migration replaced original budget snapshot: %v", err)
	}
}

func TestDurableAuthorityFailedPublicationStaysClosedAndResumesOriginalIdentity(t *testing.T) {
	dir, snapshot := durableAuthorityFixture(t)
	injected := errors.New("authority final manifest publication failed")
	original := publishAuthorityManifest
	publishAuthorityManifest = func(dir string, manifest authorityManifest, create bool) error {
		if manifest.Phase == "committed" {
			return injected
		}
		return original(dir, manifest, create)
	}
	state, err := initializeAuthorityState(dir, "local-source", snapshot)
	publishAuthorityManifest = original
	if !errors.Is(err, injected) || state != nil {
		if state != nil {
			_ = state.Journal.Close()
		}
		t.Fatalf("failed phase publication exposed authority: %+v/%v", state, err)
	}
	prepared, err := readAuthorityManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Phase != "preparing" {
		t.Fatalf("failed publication lost prepared phase: %+v", prepared)
	}
	if state, err := openAuthorityState(dir); !errors.Is(err, ErrAuthorityNotInitialized) {
		if state != nil {
			_ = state.Journal.Close()
		}
		t.Fatalf("ordinary open activated incomplete migration: %v", err)
	}
	state, err = initializeAuthorityState(dir, "local-source", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	if state.Journal.Identity() != prepared.Identity {
		t.Fatal("recovery replaced prepared identity")
	}
	a, err := state.Journal.Account(snapshot.Seeds[0].ClientID)
	if err != nil || a.Revision != 1 || a.Seed != snapshot.Seeds[0] {
		t.Fatalf("recovery replaced original migration seed: %+v/%v", a, err)
	}
}

func TestDurableAuthorityMissingCommittedJournalCannotBeReinitialized(t *testing.T) {
	dir, snapshot := durableAuthorityFixture(t)
	state, err := initializeAuthorityState(dir, "local-source", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "journal.db"), filepath.Join(dir, "preserved-activated-journal.db")); err != nil {
		t.Fatal(err)
	}
	if state, err := initializeAuthorityState(dir, "local-source", snapshot); !errors.Is(err, ErrAuthorityNotInitialized) {
		if state != nil {
			_ = state.Journal.Close()
		}
		t.Fatalf("explicit retry recreated activated journal: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "journal.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing activated authority was recreated: %v", err)
	}
}

func TestDurableAuthorityUnsafeAndContradictoryManifestNeverOpens(t *testing.T) {
	for _, fault := range []string{"permissions", "identity", "source", "snapshot", "trailing-data", "unknown-field"} {
		t.Run(fault, func(t *testing.T) {
			dir, snapshot := durableAuthorityFixture(t)
			state, err := initializeAuthorityState(dir, "local-source", snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "authority.json")
			if fault == "permissions" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if fault == "identity" || fault == "source" || fault == "snapshot" {
					manifest, err := readAuthorityManifest(dir)
					if err != nil {
						t.Fatal(err)
					}
					if fault == "identity" {
						manifest.Identity.AuthorityID = "ba518a0015870abf1e526c9d4a7ec083"
					}
					if fault == "source" {
						manifest.SourceID = "another-source"
					}
					if fault == "snapshot" {
						manifest.SnapshotDigest = strings.Repeat("a", 64)
					}
					raw, err = json.Marshal(manifest)
					if err != nil {
						t.Fatal(err)
					}
				}
				if fault == "trailing-data" {
					raw = append(raw, []byte(` {"another":true}`)...)
				}
				if fault == "unknown-field" {
					raw = append(raw[:len(raw)-1], []byte(`,"unrecognized":true}`)...)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if state, err := openAuthorityState(dir); !errors.Is(err, ErrAuthorityNotInitialized) {
				if state != nil {
					_ = state.Journal.Close()
				}
				t.Fatalf("contradictory %s manifest opened: %v", fault, err)
			}
		})
	}
}

func TestDurableAuthorityPreparedRetryPreservesPartialOrUnboundJournal(t *testing.T) {
	for _, fault := range []string{"partial", "unbound", "wrong-source", "wrong-snapshot"} {
		t.Run(fault, func(t *testing.T) {
			dir, snapshot := durableAuthorityFixture(t)
			state, err := initializeAuthorityState(dir, "local-source", snapshot)
			if err != nil {
				t.Fatal(err)
			}
			identity := state.Journal.Identity()
			if err := state.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			manifest, err := readAuthorityManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			manifest.Phase = "preparing"
			if err := writeAuthorityManifest(dir, manifest, false); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "journal.db")
			if err := os.Rename(path, filepath.Join(dir, "preserved-original-journal.db")); err != nil {
				t.Fatal(err)
			}
			if fault == "partial" {
				err = os.WriteFile(path, []byte("interrupted journal initialization"), 0600)
			} else {
				var journal *policyauthority.Journal
				if fault == "unbound" {
					journal, err = policyauthority.CreateWithMigrationIdentity(path, identity, snapshot.Seeds, snapshot.Deleted, snapshot.Records)
				} else {
					sourceID := "local-source"
					if fault == "wrong-source" {
						sourceID = "another-source"
					} else {
						snapshot.Seeds[0].Usage.BilledBytes++
					}
					journal, err = policyauthority.CreateWithMigrationSourceIdentity(path, identity, sourceID, snapshot.Seeds, snapshot.Deleted, snapshot.Records)
				}
				if err == nil {
					err = journal.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "wrong-snapshot" {
				// The retry supplies the original capture, rather than the substituted journal.
				snapshot.Seeds[0].Usage.BilledBytes--
			}
			if state, err := initializeAuthorityState(dir, "local-source", snapshot); err == nil || state != nil {
				if state != nil {
					_ = state.Journal.Close()
				}
				t.Fatalf("prepared retry accepted %s journal: %v", fault, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("prepared retry rewrote %s journal: %v", fault, err)
			}
			manifest, err = readAuthorityManifest(dir)
			if err != nil || manifest.Phase != "preparing" || manifest.Identity != identity {
				t.Fatalf("failed retry changed prepared identity or phase: %+v/%v", manifest, err)
			}
		})
	}
}
