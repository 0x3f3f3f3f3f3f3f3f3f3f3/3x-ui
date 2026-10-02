package policyauthority

import (
	"errors"
	"math"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func changeRequest(id Identity, policy Policy, requestID string, reset bool) ChangeRequest {
	return ChangeRequest{Identity: id, ClientID: "canonical-client", RequestID: requestID, ExpectedVersion: policy.Version - 1, Policy: policy, Reset: reset}
}

func TestResetBoundarySurvivesLateReportsAndLostReply(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	g, err := j.Issue(issueRequest(id, boot, "old", 60))
	if err != nil {
		t.Fatal(err)
	}
	r := Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 2, BilledBytes: 4, Remainder: 7}}
	if err := j.Report(r); err != nil {
		t.Fatal(err)
	}
	a, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	next := a.Seed.Policy
	next.WindowID, next.Version = "window-2", 2
	request := changeRequest(id, next, "reset-a", true)
	first, err := j.ChangePolicy(request)
	if err != nil {
		t.Fatal(err)
	}
	if first.UsageBoundary.BilledBytes != 14 || first.WindowUsedBefore != 14 {
		t.Fatalf("wrong boundary: %+v", first)
	}
	r.Sequence, r.Usage.RawUpload, r.Usage.BilledBytes = 2, 5, 9
	if err := j.Report(r); err != nil {
		t.Fatal(err)
	}
	retry, err := j.ChangePolicy(request)
	if err != nil || retry != first {
		t.Fatalf("retry moved reset boundary: %+v/%v", retry, err)
	}
	a, err = j.Account("canonical-client")
	if err != nil || a.WindowUsed != 5 || a.WindowRemainder != 0 || a.Usage.BilledBytes != 19 || a.Usage.Remainder != 7 || a.HeldCapacity != 50 || a.HeldRemainder != 999993 || a.Seed.Usage.BilledBytes != 10 {
		t.Fatalf("reset freed old allocation or cleared lifetime: %+v/%v", a, err)
	}
	fresh := issueRequest(id, boot, "new", 44)
	fresh.Binding.WindowID, fresh.Binding.PolicyVersion = "window-2", 2
	if _, err := j.Issue(fresh); err != nil {
		t.Fatal(err)
	}
	fresh.RequestID, fresh.Capacity = "overlap", 1
	if _, err := j.Issue(fresh); !errors.Is(err, ErrCapacity) {
		t.Fatalf("reset overlapped old held budget: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if replay, err := j.ChangePolicy(request); err != nil || replay != first {
		t.Fatalf("reopen lost durable reset: %+v/%v", replay, err)
	}
	a, err = j.Account("canonical-client")
	if err != nil || a.WindowUsed != 5 || a.HeldCapacity != 94 || a.HeldRemainder != 999993 {
		t.Fatalf("reopen changed boundary/allocation: %+v/%v", a, err)
	}
}

func TestPolicyDecreasePreservesOutstandingQuotaAndShares(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	g, err := j.Issue(issueRequest(id, boot, "old", 60))
	if err != nil {
		t.Fatal(err)
	}
	a, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	next := a.Seed.Policy
	next.Version, next.QuotaBytes = 2, 30
	next.Upload, next.Download = Direction{Rate: 200, Burst: 20}, Direction{Rate: 200, Burst: 20}
	if _, err := j.ChangePolicy(changeRequest(id, next, "decrease", false)); err != nil {
		t.Fatal(err)
	}
	fresh := issueRequest(id, boot, "blocked", 1)
	fresh.Binding.PolicyVersion = 2
	fresh.Upload, fresh.Download = Direction{Rate: 1, Burst: 1}, Direction{Rate: 1, Burst: 1}
	if _, err := j.Issue(fresh); !errors.Is(err, ErrCapacity) {
		t.Fatalf("decrease freed outstanding allocation: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.Report(Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 10, BilledBytes: 20}, Seal: true}); err != nil {
		t.Fatal(err)
	}
	next.Version, next.QuotaBytes = 3, 100
	if _, err := j.ChangePolicy(changeRequest(id, next, "increase", false)); err != nil {
		t.Fatal(err)
	}
	fresh.Binding.PolicyVersion, fresh.Capacity = 3, 70
	fresh.Upload = Direction{Rate: 201, Burst: 20}
	if _, err := j.Issue(fresh); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new grant bypassed lower rate: %v", err)
	}
	fresh.Upload, fresh.Download = next.Upload, next.Download
	if _, err := j.Issue(fresh); err != nil {
		t.Fatal(err)
	}
	a, err = j.Account("canonical-client")
	if err != nil || a.WindowUsed != 30 || a.HeldCapacity != 70 || a.Policy != next {
		t.Fatalf("policy edit changed billing: %+v/%v", a, err)
	}
}

func TestResetCannotReuseWindowOrRequestIntent(t *testing.T) {
	j, id, _, _ := journalFixture(t)
	a, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	next := a.Seed.Policy
	next.Version, next.WindowID = 2, "window-2"
	r := changeRequest(id, next, "reset", true)
	if _, err := j.ChangePolicy(r); err != nil {
		t.Fatal(err)
	}
	r.Policy.QuotaBytes++
	if _, err := j.ChangePolicy(r); !errors.Is(err, ErrRequest) {
		t.Fatalf("conflicting retry accepted: %v", err)
	}
	next.Version, next.WindowID = 3, "window-1"
	if _, err := j.ChangePolicy(changeRequest(id, next, "old-window", true)); !errors.Is(err, ErrRequest) {
		t.Fatalf("prior window reused: %v", err)
	}
	next.WindowID, next.Version = "window-3", math.MaxUint64
	if _, err := j.ChangePolicy(changeRequest(id, next, "overflow", true)); !errors.Is(err, ErrRequest) {
		t.Fatalf("unprojectable version accepted: %v", err)
	}
}

func TestPolicyTransitionHistoryCannotDisappearOnReopen(t *testing.T) {
	j, id, _, path := journalFixture(t)
	a, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	next := a.Seed.Policy
	next.Version, next.WindowID = 2, "window-2"
	r := changeRequest(id, next, "reset", true)
	if _, err := j.ChangePolicy(r); err != nil {
		t.Fatal(err)
	}
	if err := j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("changes")).Delete([]byte(compound(r.ClientID, r.RequestID)))
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, id); !errors.Is(err, ErrJournal) {
		t.Fatalf("missing reset evidence accepted: %v", err)
	}
}

func TestUnlimitedOldSharesCannotOverlapNewLimitedPolicy(t *testing.T) {
	fixture, id, boot, path := journalFixture(t)
	original, err := fixture.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	seed := original.Seed
	seed.Policy.Upload = Direction{Unlimited: true}
	j, id, err := Create(filepath.Join(filepath.Dir(path), "unlimited.db"), []Seed{seed})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	var old []Grant
	for _, requestID := range []string{"old-1", "old-2"} {
		r := issueRequest(id, boot, requestID, 10)
		r.Upload, r.Download = Direction{Unlimited: true}, Direction{}
		g, err := j.Issue(r)
		if err != nil {
			t.Fatal(err)
		}
		old = append(old, g)
	}
	next := seed.Policy
	next.Version, next.Upload = 2, Direction{Rate: 200, Burst: 20}
	if _, err := j.ChangePolicy(changeRequest(id, next, "limit", false)); err != nil {
		t.Fatal(err)
	}
	fresh := issueRequest(id, boot, "limited", 1)
	fresh.Binding.PolicyVersion, fresh.Upload, fresh.Download = 2, Direction{Rate: 100, Burst: 10}, Direction{}
	for i, g := range old {
		if _, err := j.Issue(fresh); !errors.Is(err, ErrCapacity) {
			t.Fatalf("%d unsealed unlimited grants were ignored: %v", len(old)-i, err)
		}
		if err := j.Report(Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Seal: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.Issue(fresh); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(filepath.Join(filepath.Dir(path), "unlimited.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	a, err := j.Account(seed.ClientID)
	if err != nil || a.UploadUnlimitedHeld != 0 || a.UploadHeld != fresh.Upload {
		t.Fatalf("reopen changed share ownership: %+v/%v", a, err)
	}
}

func TestExplicitResetCreditsSeedUncertaintyButRetainsGrantHold(t *testing.T) {
	fixture, _, boot, path := journalFixture(t)
	a, err := fixture.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	seed := a.Seed
	seed.FrozenBilled = 20
	j, id, err := Create(filepath.Join(filepath.Dir(path), "frozen.db"), []Seed{seed})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Issue(issueRequest(id, boot, "outstanding", 70)); err != nil {
		t.Fatal(err)
	}
	next := seed.Policy
	next.WindowID, next.Version = "new-window", 2
	change, err := j.ChangePolicy(changeRequest(id, next, "reset", true))
	if err != nil || change.FrozenBefore != 20 {
		t.Fatalf("reset lost uncertainty boundary: %+v/%v", change, err)
	}
	r := issueRequest(id, boot, "new", 30)
	r.Binding.WindowID, r.Binding.PolicyVersion = next.WindowID, next.Version
	if _, err := j.Issue(r); err != nil {
		t.Fatal(err)
	}
	a, err = j.Account(seed.ClientID)
	if err != nil || a.HeldCapacity != 100 || a.FrozenBilled != 0 || a.Seed.FrozenBilled != 20 || a.Usage.BilledBytes != 10 {
		t.Fatalf("reset discarded evidence or released outstanding grant: %+v/%v", a, err)
	}
}

func TestCumulativeReportFractionCannotRegressWithinBilledByte(t *testing.T) {
	j, id, boot, _ := journalFixture(t)
	g, err := j.Issue(issueRequest(id, boot, "fraction", 10))
	if err != nil {
		t.Fatal(err)
	}
	r := Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 1, Remainder: 700000}}
	if err := j.Report(r); err != nil {
		t.Fatal(err)
	}
	r.Sequence, r.Usage.Remainder = 2, 600000
	if err := j.Report(r); !errors.Is(err, ErrRequest) {
		t.Fatalf("cumulative fraction moved backwards: %v", err)
	}
}
