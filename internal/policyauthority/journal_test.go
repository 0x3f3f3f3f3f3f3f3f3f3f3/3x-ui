package policyauthority

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func journalFixture(t *testing.T) (*Journal, Identity, NodeBoot, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "authority")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "issuer.db")
	j, id, err := Create(path, []Seed{{ClientID: "canonical-client", Policy: Policy{WindowID: "window-1", Version: 1, QuotaBytes: 100, Upload: Direction{Rate: 1000, Burst: 100}, Download: Direction{Rate: 2000, Burst: 200}}, Usage: Usage{RawUpload: 5, RawDownload: 5, BilledBytes: 10}, WindowUsed: 10}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	boot := NodeBoot{NodeID: "node-a", SourceID: "source-a", BootID: "fresh-boot-a"}
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	return j, id, boot, path
}

func issueRequest(id Identity, boot NodeBoot, key string, capacity uint64) Request {
	return Request{Binding: Binding{Identity: id, NodeBoot: boot, ClientID: "canonical-client", WindowID: "window-1", PolicyVersion: 1}, RequestID: key, ChallengeID: "challenge-" + key, Capacity: capacity, Upload: Direction{Rate: 500, Burst: 50}, Download: Direction{Rate: 1000, Burst: 100}, LeaseDuration: time.Second}
}

func TestIssuanceCommitSurvivesReopenAndLostReply(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	r := issueRequest(id, boot, "request-a", 60)
	first, err := j.Issue(r)
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
	retry, err := reopened.Issue(r)
	if err != nil || retry != first {
		t.Fatalf("committed issuance was not replayed exactly: %+v/%v", retry, err)
	}
	if _, err := reopened.Issue(issueRequest(id, boot, "request-b", 31)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("reopen recreated committed capacity: %v", err)
	}
	r.Capacity++
	if _, err := reopened.Issue(r); !errors.Is(err, ErrRequest) {
		t.Fatalf("changed retry acquired another effect: %v", err)
	}
}

func TestConcurrentNodesCannotOverlapQuotaRateOrBurst(t *testing.T) {
	j, id, first, _ := journalFixture(t)
	second := NodeBoot{NodeID: "node-b", SourceID: "source-b", BootID: "fresh-boot-b"}
	if err := j.RegisterBoot(second); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, boot := range []NodeBoot{first, second} {
		wg.Add(1)
		go func(boot NodeBoot) {
			defer wg.Done()
			_, err := j.Issue(issueRequest(id, boot, "simultaneous", 60))
			results <- err
		}(boot)
	}
	wg.Wait()
	close(results)
	succeeded, denied := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrCapacity) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || denied != 1 {
		t.Fatalf("overlapping finite allocations: success=%d denied=%d", succeeded, denied)
	}
}

func TestReportsAreCumulativeAndOldBootCannotSeal(t *testing.T) {
	j, id, boot, _ := journalFixture(t)
	g, err := j.Issue(issueRequest(id, boot, "grant", 60))
	if err != nil {
		t.Fatal(err)
	}
	r := Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 3, RawDownload: 4, BilledBytes: 14, Remainder: 3}}
	if err := j.Report(r); err != nil {
		t.Fatal(err)
	}
	if err := j.Report(r); err != nil {
		t.Fatal(err)
	}
	a, err := j.Account("canonical-client")
	if err != nil || a.Usage.RawUpload != 8 || a.Usage.RawDownload != 9 || a.Usage.BilledBytes != 24 || a.Usage.Remainder != 3 || a.HeldCapacity != 45 || a.HeldRemainder != 999997 {
		t.Fatalf("cumulative report lost/repeated billing: %+v/%v", a, err)
	}
	boot.BootID = "replacement-boot"
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	r.Sequence++
	r.Seal = true
	if err := j.Report(r); !errors.Is(err, ErrIncarnation) {
		t.Fatalf("new boot released old unresolved allowance: %v", err)
	}
	if _, err := j.Issue(issueRequest(id, boot, "new-boot", 31)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("reboot reused unresolved grant: %v", err)
	}
}

func TestRetiredBootRegistrationCannotReactivateOldGrant(t *testing.T) {
	j, id, original, path := journalFixture(t)
	grant, err := j.Issue(issueRequest(id, original, "old-grant", 60))
	if err != nil {
		t.Fatal(err)
	}
	current := original
	current.BootID = "new-incarnation"
	if err := j.RegisterBoot(current); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.RegisterBoot(original); !errors.Is(err, ErrIncarnation) {
		t.Errorf("delayed registration reactivated a retired boot: %v", err)
	}
	if err := j.Report(Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Seal: true}); !errors.Is(err, ErrIncarnation) {
		t.Errorf("retired registration released unresolved allowance: %v", err)
	}
}
