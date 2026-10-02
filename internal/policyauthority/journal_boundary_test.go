package policyauthority

import (
	"errors"
	bolt "go.etcd.io/bbolt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRateBurstAndZeroShareAreIndependentOfQuota(t *testing.T) {
	for _, field := range []string{"rate", "burst", "download"} {
		t.Run(field, func(t *testing.T) {
			j, id, boot, _ := journalFixture(t)
			first := issueRequest(id, boot, "first", 1)
			first.Upload = Direction{Rate: 900, Burst: 90}
			first.Download = Direction{Rate: 1900, Burst: 190}
			if _, err := j.Issue(first); err != nil {
				t.Fatal(err)
			}
			second := issueRequest(id, boot, "second", 1)
			second.Upload = Direction{Rate: 100, Burst: 10}
			second.Download = Direction{Rate: 100, Burst: 10}
			switch field {
			case "rate":
				second.Upload.Rate++
			case "burst":
				second.Upload.Burst++
			case "download":
				second.Download.Rate++
			}
			if _, err := j.Issue(second); !errors.Is(err, ErrCapacity) {
				t.Fatalf("%s shares exceeded global bound: %v", field, err)
			}
			second.Upload = Direction{}
			second.Download = Direction{}
			g, err := j.Issue(second)
			if err != nil || g.Request.Upload.Unlimited || g.Request.Download.Unlimited {
				t.Fatalf("zero share confused with unlimited: %+v/%v", g, err)
			}
			forged := issueRequest(id, boot, "forged-unlimited", 1)
			forged.Upload = Direction{Unlimited: true}
			if _, err := j.Issue(forged); !errors.Is(err, ErrCapacity) {
				t.Fatalf("limited policy accepted unlimited direction: %v", err)
			}
		})
	}
}

func TestCurrentSealReleasesOnlyVerifiedUnusedCapacity(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	g, err := j.Issue(issueRequest(id, boot, "first", 60))
	if err != nil {
		t.Fatal(err)
	}
	report := Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 8, RawDownload: 2, BilledBytes: 20, Remainder: 123}, Seal: true}
	if err := j.Report(report); err != nil {
		t.Fatal(err)
	}
	if err := j.Report(report); err != nil {
		t.Fatal(err)
	}
	a, err := j.Account("canonical-client")
	if err != nil || a.Usage.BilledBytes != 30 || a.Usage.Remainder != 123 || a.WindowUsed != 30 || a.WindowRemainder != 123 || a.HeldCapacity != 0 || a.HeldRemainder != 0 || a.UploadHeld.Rate != 0 || a.DownloadHeld.Burst != 0 || a.Seed.Usage.BilledBytes != 10 {
		t.Fatalf("seal lost history or double released: %+v/%v", a, err)
	}
	if _, err := j.Issue(issueRequest(id, boot, "rounded-up", 70)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("fractional remaining budget was rounded up: %v", err)
	}
	if _, err := j.Issue(issueRequest(id, boot, "remaining", 69)); err != nil {
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
	restored, err := reopened.Issue(g.Request)
	if err != nil || !restored.Sealed || restored.Usage != report.Usage {
		t.Fatalf("reopen lost source fraction/seal: %+v/%v", restored, err)
	}
}

func TestExpiredAndLostGrantRemainsUnavailable(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	r := issueRequest(id, boot, "expired", 60)
	r.LeaseDuration = time.Nanosecond
	if _, err := j.Issue(r); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Issue(issueRequest(id, boot, "new", 31)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("clock/reopen freed unproven capacity: %v", err)
	}
}

func TestJournalNeverRecreatesMissingOrMismatchedAuthority(t *testing.T) {
	j, id, _, path := journalFixture(t)
	if _, _, err := Create(path, nil); !errors.Is(err, ErrJournal) {
		t.Fatalf("creation overwrote active authority: %v", err)
	}
	if _, err := Open(path, id); !errors.Is(err, ErrJournal) {
		t.Fatalf("second concurrent open was not excluded: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	wrong := id
	wrong.Generation++
	if _, err := Open(path, wrong); !errors.Is(err, ErrIdentity) {
		t.Fatalf("wrong authority accepted: %v", err)
	}
	if err := os.Rename(path, path+".retained"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, id); !errors.Is(err, ErrJournal) {
		t.Fatalf("missing authority silently initialized: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening missing journal created a file: %v", err)
	}
	// An ordinary shared directory is unsuitable for authority initialization.
	if _, _, err := Create(filepath.Join(t.TempDir(), "shared.db"), nil); !errors.Is(err, ErrJournal) {
		t.Fatalf("shared journal directory accepted: %v", err)
	}
}

func TestJournalReopenRejectsContradictoryCapacity(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	if _, err := j.Issue(issueRequest(id, boot, "first", 10)); err != nil {
		t.Fatal(err)
	}
	// Mutate only this test's owned journal, modeling a contradictory snapshot.
	if err := j.db.Update(func(tx *bolt.Tx) error {
		var a Account
		if err := get(tx, "accounts", "canonical-client", &a); err != nil {
			return err
		}
		a.HeldCapacity--
		return put(tx, "accounts", "canonical-client", a)
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, id); !errors.Is(err, ErrJournal) {
		t.Fatalf("contradictory journal recreated free capacity: %v", err)
	}
}

func TestDeletedIdentityCannotBeResurrectedFromOldSeed(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	original, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := j.Issue(issueRequest(id, boot, "before-delete", 60))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Tombstone("canonical-client"); err != nil {
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
	if err := reopened.AddAccount(original.Seed); !errors.Is(err, ErrDeleted) {
		t.Fatalf("old SQL seed resurrected canonical identity: %v", err)
	}
	if _, err := reopened.Issue(issueRequest(id, boot, "after-delete", 1)); !errors.Is(err, ErrDeleted) {
		t.Fatalf("deleted account acquired a new grant: %v", err)
	}
	if err := reopened.Report(Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Usage: Usage{RawUpload: 2, BilledBytes: 4}}); err != nil {
		t.Fatal(err)
	}
	a, err := reopened.Account("canonical-client")
	if err != nil || !a.Deleted || a.Usage.BilledBytes != 14 || a.HeldCapacity != 56 {
		t.Fatalf("deletion discarded confirmed/outstanding history: %+v/%v", a, err)
	}
	newSeed := original.Seed
	newSeed.ClientID = "new-canonical-identity"
	if err := reopened.AddAccount(newSeed); err != nil {
		t.Fatalf("explicit new identity could not be provisioned: %v", err)
	}
}

func TestProvisioningRetryNeverOverwritesConfirmedUsage(t *testing.T) {
	j, id, boot, _ := journalFixture(t)
	initial, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	g, err := j.Issue(issueRequest(id, boot, "prior-consumption", 10))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Report(Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 2, BilledBytes: 4}}); err != nil {
		t.Fatal(err)
	}
	if err := j.AddAccount(initial.Seed); err != nil {
		t.Errorf("lost provisioning response could not retry exact original seed: %v", err)
	}
	current, err := j.Account("canonical-client")
	if err != nil || current.Usage.BilledBytes != 14 || current.HeldCapacity != 6 {
		t.Fatalf("provisioning retry reset already committed history: %+v/%v", current, err)
	}
	changed := initial.Seed
	changed.Usage.BilledBytes--
	if err := j.AddAccount(changed); !errors.Is(err, ErrRequest) {
		t.Fatalf("changed initialization intent reused canonical ID: %v", err)
	}
}

func TestJournalDisappearingBeforeOpenIsNeverRecreated(t *testing.T) {
	j, id, _, path := journalFixture(t)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	previous := openJournal
	t.Cleanup(func() { openJournal = previous })
	openJournal = func(path string, mode os.FileMode, options *bolt.Options) (*bolt.DB, error) {
		if err := os.Rename(path, path+".retained"); err != nil {
			return nil, err
		}
		return previous(path, mode, options)
	}
	if _, err := Open(path, id); !errors.Is(err, ErrJournal) {
		t.Fatalf("missing activated journal reopened: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open recreated a lost authority file: %v", err)
	}
}

func TestDeletedAccountCannotReplayUnsealedIssuance(t *testing.T) {
	j, id, boot, _ := journalFixture(t)
	r := issueRequest(id, boot, "before-delete", 10)
	if _, err := j.Issue(r); err != nil {
		t.Fatal(err)
	}
	if err := j.Tombstone(r.Binding.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Issue(r); !errors.Is(err, ErrDeleted) {
		t.Fatalf("deleted client replayed an unsealed grant: %v", err)
	}
}
