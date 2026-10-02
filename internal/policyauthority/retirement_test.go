package policyauthority

import (
	"errors"
	bolt "go.etcd.io/bbolt"
	"sync"
	"testing"
	"time"
)

func TestRetiredRatesWaitFullMonotonicIntervalWithoutReleasingQuota(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	r := issueRequest(id, boot, "lost-core", 60)
	r.Upload, r.Download = Direction{Rate: 1000, Burst: 100}, Direction{Rate: 2000, Burst: 200}
	grant, err := j.Issue(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Report(Report{Binding: r.Binding, GrantID: grant.GrantID, Sequence: 1, Usage: Usage{RawUpload: 1, BilledBytes: 2, Remainder: 500000}}); err != nil {
		t.Fatal(err)
	}
	before, err := j.Account(r.Binding.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := MaxLeaseDuration
	j.retirementElapsed = func(time.Time) time.Duration { return elapsed }
	if err := j.ReleaseRetiredRates(boot); !errors.Is(err, ErrIncarnation) {
		t.Fatalf("current boot released its live shares: %v", err)
	}
	next := boot
	next.BootID = "next-boot"
	if err := j.RegisterBoot(next); err != nil {
		t.Fatal(err)
	}
	elapsed = MaxLeaseDuration - time.Nanosecond
	if err := j.ReleaseRetiredRates(boot); !errors.Is(err, ErrCapacity) {
		t.Fatalf("released old rates before maximum execution interval: %v", err)
	}
	request := issueRequest(id, next, "replacement", 30)
	request.Upload, request.Download = r.Upload, r.Download
	if _, err := j.Issue(request); !errors.Is(err, ErrCapacity) {
		t.Fatalf("retired rates overlapped before quarantine: %v", err)
	}
	elapsed = MaxLeaseDuration
	if err := j.ReleaseRetiredRates(boot); err != nil {
		t.Fatal(err)
	}
	after, err := j.Account(r.Binding.ClientID)
	if err != nil || after.Usage != before.Usage || after.WindowUsed != before.WindowUsed || after.WindowRemainder != before.WindowRemainder || after.HeldCapacity != before.HeldCapacity || after.HeldRemainder != before.HeldRemainder || after.FrozenBilled != before.FrozenBilled || after.UploadHeld != (Direction{}) || after.DownloadHeld != (Direction{}) || after.Revision != before.Revision+1 {
		t.Fatalf("rate retirement released uncertain quota or altered accounting: %+v/%+v/%v", before, after, err)
	}
	if err := j.ReleaseRetiredRates(boot); err != nil {
		t.Fatal(err)
	}
	duplicate, err := j.Account(r.Binding.ClientID)
	if err != nil || duplicate != after {
		t.Fatalf("duplicate retirement changed history: %+v/%v", duplicate, err)
	}
	request.Capacity = 31
	if _, err := j.Issue(request); !errors.Is(err, ErrCapacity) {
		t.Fatalf("quarantine recreated lost quota: %v", err)
	}
	request.Capacity = 30
	if _, err := j.Issue(request); err != nil {
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
	if err := reopened.ReleaseRetiredRates(boot); err != nil {
		t.Fatalf("durable retirement was lost on reopen: %v", err)
	}
	account, err := reopened.Account(r.Binding.ClientID)
	if err != nil || account.HeldCapacity != 87 || account.HeldRemainder != 500000 || account.UploadHeld != request.Upload {
		t.Fatalf("reopen lost frozen old capacity/new rates: %+v/%v", account, err)
	}
}

func TestRetiredClientRatePagesAreBoundedIdempotentAndNodeScoped(t *testing.T) {
	j, id, old, _ := journalFixture(t)
	first, err := j.Issue(issueRequest(id, old, "a", 10))
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.Issue(issueRequest(id, old, "b", 10))
	if err != nil {
		t.Fatal(err)
	}
	other := NodeBoot{NodeID: "node-b", SourceID: "source-b", BootID: "other-boot"}
	if err := j.RegisterBoot(other); err != nil {
		t.Fatal(err)
	}
	request := issueRequest(id, other, "other", 1)
	request.Upload, request.Download = Direction{}, Direction{}
	otherGrant, err := j.Issue(request)
	if err != nil {
		t.Fatal(err)
	}
	current := old
	current.BootID = "replacement-boot"
	if err := j.RegisterBoot(current); err != nil {
		t.Fatal(err)
	}
	elapsed := MaxLeaseDuration - time.Nanosecond
	j.retirementElapsed = func(time.Time) time.Duration { return elapsed }
	before, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	if next, err := j.ReleaseRetiredClientRatesPage(old.NodeID, "canonical-client", "", 1); err != nil || next != "a" {
		t.Fatalf("bounded first page: %q/%v", next, err)
	}
	if account, err := j.Account("canonical-client"); err != nil || account != before {
		t.Fatalf("page released rates before the monotonic boundary: %+v/%v", account, err)
	}
	elapsed = MaxLeaseDuration
	if next, err := j.ReleaseRetiredClientRatesPage(old.NodeID, "canonical-client", "", 1); err != nil || next != "a" {
		t.Fatalf("first release page: %q/%v", next, err)
	}
	afterFirst, err := j.Account("canonical-client")
	if err != nil || afterFirst.HeldCapacity != before.HeldCapacity || afterFirst.Usage != before.Usage || afterFirst.UploadHeld.Rate != 500 || afterFirst.DownloadHeld.Rate != 1000 || afterFirst.Revision != before.Revision+1 {
		t.Fatalf("page crossed its limit or credited quota: %+v/%v", afterFirst, err)
	}
	if _, err := j.ReleaseRetiredClientRatesPage(old.NodeID, "canonical-client", "", 1); err != nil {
		t.Fatal(err)
	}
	if retry, err := j.Account("canonical-client"); err != nil || retry != afterFirst {
		t.Fatalf("page retry changed committed accounting: %+v/%v", retry, err)
	}
	if next, err := j.ReleaseRetiredClientRatesPage(old.NodeID, "canonical-client", "a", 1); err != nil || next != "" {
		t.Fatalf("terminal page: %q/%v", next, err)
	}
	final, err := j.Account("canonical-client")
	if err != nil || final.HeldCapacity != before.HeldCapacity || final.Usage != before.Usage || final.UploadHeld.Rate != 0 || final.DownloadHeld.Rate != 0 || final.Revision != before.Revision+2 {
		t.Fatalf("terminal release changed uncertain quota or totals: %+v/%v", final, err)
	}
	for _, grant := range []Grant{first, second} {
		retired, err := j.Grant(grant.GrantID)
		if err != nil || retired.Sealed || !retired.RatesReleased {
			t.Fatalf("retired grant state: %+v/%v", retired, err)
		}
	}
	if untouched, err := j.Grant(otherGrant.GrantID); err != nil || untouched != otherGrant {
		t.Fatalf("page changed another node's grant: %+v/%v", untouched, err)
	}
}

func TestRetiredUnlimitedSharesNoLongerBlockLimitedPolicy(t *testing.T) {
	j, id, boot, _ := journalFixture(t)
	a, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	p := a.Policy
	p.Version++
	p.Upload = Direction{Unlimited: true}
	p.Download = p.Upload
	if _, err := j.ChangePolicy(ChangeRequest{Identity: id, ClientID: a.Seed.ClientID, RequestID: "unlimited", ExpectedVersion: 1, Policy: p}); err != nil {
		t.Fatal(err)
	}
	r := issueRequest(id, boot, "lost-unlimited", 60)
	r.Binding.PolicyVersion = 2
	r.Upload = p.Upload
	r.Download = p.Download
	if _, err := j.Issue(r); err != nil {
		t.Fatal(err)
	}
	p.Version++
	p.Upload = a.Policy.Upload
	p.Download = a.Policy.Download
	if _, err := j.ChangePolicy(ChangeRequest{Identity: id, ClientID: a.Seed.ClientID, RequestID: "limited", ExpectedVersion: 2, Policy: p}); err != nil {
		t.Fatal(err)
	}
	next := boot
	next.BootID = "next-limited"
	if err := j.RegisterBoot(next); err != nil {
		t.Fatal(err)
	}
	r = issueRequest(id, next, "limited", 30)
	r.Binding.PolicyVersion = 3
	if _, err := j.Issue(r); !errors.Is(err, ErrCapacity) {
		t.Fatalf("old unlimited grant overlapped limited policy: %v", err)
	}
	j.retirementElapsed = func(time.Time) time.Duration { return MaxLeaseDuration }
	if err := j.ReleaseRetiredRates(boot); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Issue(r); err != nil {
		t.Fatal(err)
	}
	a, err = j.Account(r.Binding.ClientID)
	if err != nil || a.HeldCapacity != 90 || a.UploadUnlimitedHeld != 0 || a.DownloadUnlimitedHeld != 0 {
		t.Fatalf("unlimited retirement changed quota/retained old direction: %+v/%v", a, err)
	}
}

func TestJournalRejectsRateRetirementForCurrentOrSealedGrant(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "sealed"}[sealed], func(t *testing.T) {
			j, id, boot, path := journalFixture(t)
			grant, err := j.Issue(issueRequest(id, boot, "corrupt-retirement", 60))
			if err != nil {
				t.Fatal(err)
			}
			if sealed {
				if err := j.Report(Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Seal: true}); err != nil {
					t.Fatal(err)
				}
				grant, err = j.Grant(grant.GrantID)
				if err != nil {
					t.Fatal(err)
				}
				next := boot
				next.BootID = "next"
				if err := j.RegisterBoot(next); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.db.Update(func(tx *bolt.Tx) error {
				var a Account
				if err := get(tx, "accounts", grant.Request.Binding.ClientID, &a); err != nil {
					return err
				}
				if !sealed {
					if err := releaseGrantRates(&a, grant.Request); err != nil {
						return err
					}
				}
				if err := advanceRevision(&a); err != nil {
					return err
				}
				grant.RatesReleased = true
				if err := put(tx, "grants", grant.GrantID, grant); err != nil {
					return err
				}
				return put(tx, "accounts", a.Seed.ClientID, a)
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
				t.Fatalf("contradictory retirement opened: %v", err)
			}
		})
	}
}

func TestRetiredRatesReopenRestartsQuarantineWithoutWallClockCredit(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	if _, err := j.Issue(issueRequest(id, boot, "lost", 60)); err != nil {
		t.Fatal(err)
	}
	next := boot
	next.BootID = "replacement"
	if err := j.RegisterBoot(next); err != nil {
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
	if err := reopened.ReleaseRetiredRates(boot); !errors.Is(err, ErrCapacity) {
		t.Fatalf("reopen credited old wall clock expiry: %v", err)
	}
	before, err := reopened.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ReleaseRetiredClientRatesPage(boot.NodeID, "canonical-client", "", 1000); err != nil {
		t.Fatal(err)
	}
	if account, err := reopened.Account("canonical-client"); err != nil || account != before {
		t.Fatalf("reopened rate page reused an old deadline: %+v/%v", account, err)
	}
	elapsed := MaxLeaseDuration - time.Nanosecond
	reopened.retirementElapsed = func(time.Time) time.Duration { return elapsed }
	if err := reopened.ReleaseRetiredRates(boot); !errors.Is(err, ErrCapacity) {
		t.Fatalf("reopen shortened full monotonic quarantine: %v", err)
	}
	if _, err := reopened.ReleaseRetiredClientRatesPage(boot.NodeID, "canonical-client", "", 1000); err != nil {
		t.Fatal(err)
	}
	if account, err := reopened.Account("canonical-client"); err != nil || account != before {
		t.Fatalf("reopened rate page shortened quarantine: %+v/%v", account, err)
	}
	elapsed = MaxLeaseDuration
	if _, err := reopened.ReleaseRetiredClientRatesPage(boot.NodeID, "canonical-client", "", 1000); err != nil {
		t.Fatal(err)
	}
	committed, err := reopened.Account("canonical-client")
	if err != nil || committed.HeldCapacity != before.HeldCapacity || committed.Usage != before.Usage || committed.UploadHeld.Rate != 0 || committed.DownloadHeld.Rate != 0 {
		t.Fatalf("reopened rate page credited quota or usage: %+v/%v", committed, err)
	}
	if err := reopened.ReleaseRetiredRates(boot); err != nil {
		t.Fatal(err)
	}
	if account, err := reopened.Account("canonical-client"); err != nil || account != committed {
		t.Fatalf("mixed release APIs duplicated the durable effect: %+v/%v", account, err)
	}
}

func TestConcurrentRetiredRateReleaseIsOneDurableEffect(t *testing.T) {
	j, id, boot, _ := journalFixture(t)
	if _, err := j.Issue(issueRequest(id, boot, "lost", 60)); err != nil {
		t.Fatal(err)
	}
	next := boot
	next.BootID = "replacement"
	if err := j.RegisterBoot(next); err != nil {
		t.Fatal(err)
	}
	j.retirementElapsed = func(time.Time) time.Duration { return MaxLeaseDuration }
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				results <- j.ReleaseRetiredRates(boot)
			} else {
				_, err := j.ReleaseRetiredClientRatesPage(boot.NodeID, "canonical-client", "", 1)
				results <- err
			}
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	a, err := j.Account("canonical-client")
	if err != nil || a.HeldCapacity != 60 || a.Revision != 3 || a.UploadHeld != (Direction{}) {
		t.Fatalf("concurrent retirement double effect: %+v/%v", a, err)
	}
}
