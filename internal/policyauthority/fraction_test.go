package policyauthority

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
)

func TestSealedFractionRemainsChargedAgainstQuota(t *testing.T) {
	j, id, boot, _ := journalFixture(t)
	g, err := j.Issue(issueRequest(id, boot, "fraction", 90))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Report(Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 179, BilledBytes: 89, Remainder: 500000}, Seal: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Issue(issueRequest(id, boot, "extra", 1)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("sealed half-byte was refunded as a whole byte: %v", err)
	}
}

func TestGrantCannotReportFractionBeyondItsCapacity(t *testing.T) {
	j, id, boot, _ := journalFixture(t)
	g, err := j.Issue(issueRequest(id, boot, "fraction", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Report(Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 3, BilledBytes: 1, Remainder: 1}}); !errors.Is(err, ErrRequest) {
		t.Fatalf("fraction exceeded finite grant: %v", err)
	}
}

func TestGrantFractionsCarryWithoutOverlappingRemainingCapacity(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	var grants []Grant
	for i, capacity := range []uint64{40, 50} {
		requestID := []string{"a", "b"}[i]
		g, err := j.Issue(issueRequest(id, boot, requestID, capacity))
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, g)
		if err := j.Report(Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 1, Remainder: 500000}}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := j.Account("canonical-client")
	if err != nil || a.Usage.BilledBytes != 11 || a.Usage.Remainder != 0 || a.HeldCapacity != 89 || a.HeldRemainder != 0 {
		t.Fatalf("two half bytes lost carry/remaining budget: %+v/%v", a, err)
	}
	r := issueRequest(id, boot, "extra", 1)
	r.Upload, r.Download = Direction{}, Direction{}
	if _, err := j.Issue(r); !errors.Is(err, ErrCapacity) {
		t.Fatalf("unsealed fractions created capacity: %v", err)
	}
	for i, g := range grants {
		fraction := uint64(500000)
		if i == 0 {
			fraction = 600000
		}
		if err := j.Report(Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 2, Usage: Usage{RawUpload: 2, Remainder: fraction}, Seal: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	a, err = j.Account("canonical-client")
	if err != nil || a.Usage.BilledBytes != 11 || a.Usage.Remainder != 100000 || a.WindowUsed != 11 || a.WindowRemainder != 100000 || a.HeldCapacity != 0 || a.HeldRemainder != 0 {
		t.Fatalf("reopen lost exact confirmed fractions: %+v/%v", a, err)
	}
	r.Capacity = 89
	if _, err := j.Issue(r); !errors.Is(err, ErrCapacity) {
		t.Fatalf("aggregate fraction allowed oversized grant: %v", err)
	}
	r.Capacity = 88
	if _, err := j.Issue(r); err != nil {
		t.Fatal(err)
	}
}

func TestFractionCarryNearSQLLimitRejectsWholeTransaction(t *testing.T) {
	fixture, _, boot, path := journalFixture(t)
	a, err := fixture.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	seed := a.Seed
	seed.Usage = Usage{BilledBytes: math.MaxInt64 - 1, Remainder: 900000}
	seed.WindowUsed = 0
	j, id, err := Create(filepath.Join(filepath.Dir(path), "max-int.db"), []Seed{seed})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	g, err := j.Issue(issueRequest(id, boot, "max", 2))
	if err != nil {
		t.Fatal(err)
	}
	r := Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 1, Remainder: 200000}}
	if err := j.Report(r); err != nil {
		t.Fatal(err)
	}
	before, err := j.Account(seed.ClientID)
	if err != nil || before.Usage.BilledBytes != math.MaxInt64 || before.Usage.Remainder != 100000 {
		t.Fatalf("valid carry overflowed/truncated: %+v/%v", before, err)
	}
	r.Sequence, r.Usage.RawUpload, r.Usage.BilledBytes = 2, 2, 1
	if err := j.Report(r); !errors.Is(err, ErrCapacity) {
		t.Fatalf("unprojectable carry accepted: %v", err)
	}
	a, err = j.Account(seed.ClientID)
	if err != nil || a != before {
		t.Fatalf("failed report partially advanced accounting: %+v/%v", a, err)
	}
}
