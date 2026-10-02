package policyauthority

import "testing"

func TestAccountRevisionAdvancesOnlyOncePerCommittedEffect(t *testing.T) {
	j, id, boot, _ := journalFixture(t)
	a, err := j.Account("canonical-client")
	if err != nil || a.Revision != 1 {
		t.Fatalf("initial revision: %+v/%v", a, err)
	}
	r := issueRequest(id, boot, "grant", 10)
	g, err := j.Issue(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Issue(r); err != nil {
		t.Fatal(err)
	}
	a, err = j.Account("canonical-client")
	if err != nil || a.Revision != 2 {
		t.Fatalf("issuance/retry revision: %d/%v", a.Revision, err)
	}
	report := Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: Usage{RawUpload: 1, BilledBytes: 1}, Seal: true}
	if err := j.Report(report); err != nil {
		t.Fatal(err)
	}
	if err := j.Report(report); err != nil {
		t.Fatal(err)
	}
	a, err = j.Account("canonical-client")
	if err != nil || a.Revision != 3 {
		t.Fatalf("report/retry revision: %d/%v", a.Revision, err)
	}
	next := a.Policy
	next.Version = 2
	change := changeRequest(id, next, "edit", false)
	if _, err := j.ChangePolicy(change); err != nil {
		t.Fatal(err)
	}
	if _, err := j.ChangePolicy(change); err != nil {
		t.Fatal(err)
	}
	a, err = j.Account("canonical-client")
	if err != nil || a.Revision != 4 {
		t.Fatalf("change/retry revision: %d/%v", a.Revision, err)
	}
	if err := j.Tombstone(a.Seed.ClientID); err != nil {
		t.Fatal(err)
	}
	if err := j.Tombstone(a.Seed.ClientID); err != nil {
		t.Fatal(err)
	}
	a, err = j.Account("canonical-client")
	if err != nil || a.Revision != 5 {
		t.Fatalf("tombstone/retry revision: %d/%v", a.Revision, err)
	}
}
