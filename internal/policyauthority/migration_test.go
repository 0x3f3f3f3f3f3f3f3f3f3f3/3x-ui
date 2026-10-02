package policyauthority

import (
	"bytes"
	"errors"
	bolt "go.etcd.io/bbolt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migrationFixture(t *testing.T) (string, []Seed, []MigrationRecord) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private-migration")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := Policy{WindowID: "original-window", Version: 7, QuotaBytes: 100, Upload: Direction{Unlimited: true}, Download: Direction{Unlimited: true}}
	seeds := []Seed{{ClientID: "retained", Policy: p, Usage: Usage{RawUpload: 3, RawDownload: 2, BilledBytes: 10, Remainder: 500000}, WindowUsed: 4, WindowRemainder: 250000, FrozenBilled: 20}, {ClientID: "deleted", Policy: p, Usage: Usage{RawUpload: 1, BilledBytes: 2}}}
	records := []MigrationRecord{{Kind: "receipts", Key: "local/retained", Value: []byte(`{"seedBilled":5,"billedBytes":10,"remainder":500000,"reservedBytes":20}`)}, {Kind: "resets", Key: "retained/original-reset", Value: []byte(`{"requestId":"original-reset","billedBytes":6,"remainder":250000}`)}, {Kind: "tombstones", Key: "deleted", Value: []byte(`{"clientId":"deleted","createdAt":1}`)}}
	return filepath.Join(dir, "journal.db"), seeds, records
}

func TestMigrationPreservesLargeResetMembershipRecord(t *testing.T) {
	path, seeds, records := migrationFixture(t)
	// Existing bulk reset rows exceed the normal per-account journal limit.
	record := MigrationRecord{Kind: "reset-batches", Key: "original-bulk-reset", Value: []byte(`{"targets":"` + strings.Repeat("retained,", 10000) + `"}`)}
	records = append(records, record)
	j, id, err := CreateWithMigration(path, seeds, nil, records)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.MigrationRecord(record.Kind, record.Key)
	if err != nil || !bytes.Equal(got.Value, record.Value) {
		t.Fatalf("bulk reset membership truncated: %d/%v", len(got.Value), err)
	}
}

func TestMigrationFailedCommitRetainsUnusableFileWithoutRecreation(t *testing.T) {
	path, seeds, records := migrationFixture(t)
	original := openJournal
	openJournal = func(path string, mode os.FileMode, options *bolt.Options) (*bolt.DB, error) {
		db, err := original(path, mode, options)
		if err != nil {
			return nil, err
		}
		if err := db.Close(); err != nil {
			return nil, err
		}
		return db, nil
	}
	j, _, err := CreateWithMigration(path, seeds, nil, records)
	openJournal = original
	if j != nil || !errors.Is(err, ErrJournal) {
		if j != nil {
			_ = j.Close()
		}
		t.Fatalf("failed migration exposed authority: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("failed migration evidence removed: %v", err)
	}
	if j, _, err := CreateWithMigration(path, seeds, nil, records); err == nil {
		if j != nil {
			_ = j.Close()
		}
		t.Fatal("failed migration was silently recreated")
	}
}

func TestMigrationCreatesSeedsDeletionAndOriginalEvidenceAtomically(t *testing.T) {
	path, seeds, records := migrationFixture(t)
	j, id, err := CreateWithMigration(path, seeds, []string{"deleted"}, records)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	a, err := j.Account("retained")
	if err != nil || a.Seed != seeds[0] || a.FrozenBilled != 20 || a.Usage.Remainder != 500000 || a.WindowRemainder != 250000 {
		t.Fatalf("migration changed original charged boundary: %+v/%v", a, err)
	}
	d, err := j.Account("deleted")
	if err != nil || !d.Deleted || d.Seed != seeds[1] || d.Revision != 2 {
		t.Fatalf("migration lost terminal identity/history: %+v/%v", d, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, record := range records {
		got, err := reopened.MigrationRecord(record.Kind, record.Key)
		if err != nil || got.Kind != record.Kind || got.Key != record.Key || !bytes.Equal(got.Value, record.Value) {
			t.Fatalf("original migration evidence lost: %+v/%v", got, err)
		}
	}
	if _, err := reopened.MigrationRecord("receipts", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing original evidence: %v", err)
	}
	if _, _, err := CreateWithMigration(path, seeds, nil, nil); err == nil {
		t.Fatal("migration overwrote an existing authority")
	}
}

func TestMigrationPreparedIdentitySurvivesReopenAndRejectsWrongManifest(t *testing.T) {
	path, seeds, records := migrationFixture(t)
	id := Identity{AuthorityID: "a3e5ddbd913729c9afb7ed580e078d20", Generation: 1}
	j, err := CreateWithMigrationIdentity(path, id, seeds, []string{"deleted"}, records)
	if err != nil {
		t.Fatal(err)
	}
	if j.Identity() != id {
		_ = j.Close()
		t.Fatalf("migration replaced prepared identity: %+v", j.Identity())
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	wrong := id
	wrong.AuthorityID = "another-prepared-authority"
	if opened, err := Open(path, wrong); !errors.Is(err, ErrIdentity) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("mismatched migration manifest opened authority: %v", err)
	}
}

func TestMigrationIdentityPreflightNeverCreatesUnpreparedOrNewGenerationAuthority(t *testing.T) {
	for _, id := range []Identity{{}, {AuthorityID: "prepared", Generation: 0}, {AuthorityID: "prepared", Generation: 2}} {
		path, seeds, records := migrationFixture(t)
		if j, err := CreateWithMigrationIdentity(path, id, seeds, nil, records); !errors.Is(err, ErrIdentity) {
			if j != nil {
				_ = j.Close()
			}
			t.Fatalf("invalid initial identity created authority: %+v/%v", id, err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("identity preflight left state: %v", err)
		}
	}
}

func TestMigrationInvalidMembershipAndEvidenceLeavesNoAuthority(t *testing.T) {
	for _, fault := range []string{"unknown-deletion", "duplicate-deletion", "duplicate-record", "invalid-json", "unknown-kind"} {
		t.Run(fault, func(t *testing.T) {
			path, seeds, records := migrationFixture(t)
			deleted := []string{"deleted"}
			switch fault {
			case "unknown-deletion":
				deleted = []string{"missing"}
			case "duplicate-deletion":
				deleted = append(deleted, "deleted")
			case "duplicate-record":
				records = append(records, records[0])
			case "invalid-json":
				records[0].Value = []byte(`{"bad":`)
			case "unknown-kind":
				records[0].Kind = "business-secrets"
			}
			if j, _, err := CreateWithMigration(path, seeds, deleted, records); !errors.Is(err, ErrRequest) {
				if j != nil {
					_ = j.Close()
				}
				t.Fatalf("invalid migration committed: %v", err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid migration left partial authority: %v", err)
			}
		})
	}
}

func TestMigrationReopenRejectsChangedOrRemovedOriginalEvidence(t *testing.T) {
	for _, fault := range []string{"change", "remove"} {
		t.Run(fault, func(t *testing.T) {
			path, seeds, records := migrationFixture(t)
			j, id, err := CreateWithMigration(path, seeds, nil, records)
			if err != nil {
				t.Fatal(err)
			}
			if err := j.db.Update(func(tx *bolt.Tx) error {
				bucket := tx.Bucket([]byte("migration"))
				k := []byte(compound(records[0].Kind, records[0].Key))
				if fault == "remove" {
					return bucket.Delete(k)
				}
				record := records[0]
				record.Value = []byte(`{"billedBytes":0}`)
				return putMigrationRecord(tx, record)
			}); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path, id); !errors.Is(err, ErrJournal) {
				if reopened != nil {
					_ = reopened.Close()
				}
				t.Fatalf("changed migration history opened: %v", err)
			}
		})
	}
}
