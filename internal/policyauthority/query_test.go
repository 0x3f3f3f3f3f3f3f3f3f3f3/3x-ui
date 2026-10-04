package policyauthority

import (
	"errors"
	"fmt"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestGrantPagesRetainOriginalReceiptsForBoundedRecovery(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	issued := make(map[string]Grant)
	for i := range 19 {
		request := issueRequest(id, boot, fmt.Sprintf("recover-%02d", i), 1)
		request.Upload, request.Download = Direction{}, Direction{}
		grant, err := j.Issue(request)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := j.Report(Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Usage: Usage{RawUpload: 1, BilledBytes: 1}, Seal: true}); err != nil {
				t.Fatal(err)
			}
			grant, err = j.Grant(grant.GrantID)
			if err != nil {
				t.Fatal(err)
			}
		}
		issued[grant.GrantID] = grant
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	api, ok := any(opened).(interface {
		GrantPage(string, int) ([]Grant, error)
	})
	if !ok {
		t.Fatal("original grants cannot be read in bounded pages after coordinator restart")
	}
	seen, cursor := make(map[string]bool), ""
	for {
		page, err := api.GrantPage(cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 3 {
			t.Fatal("grant recovery page exceeded its bound")
		}
		for _, grant := range page {
			if seen[grant.GrantID] || issued[grant.GrantID] != grant {
				t.Fatal("grant recovery page lost or duplicated original receipt")
			}
			seen[grant.GrantID] = true
		}
		if len(page) < 3 {
			break
		}
		cursor = page[len(page)-1].GrantID
	}
	if len(seen) != len(issued) {
		t.Fatalf("recovery missed original grants: %d/%d", len(seen), len(issued))
	}
	for _, limit := range []int{0, 129} {
		if _, err := api.GrantPage("", limit); err == nil {
			t.Fatal("unbounded grant page accepted")
		}
	}
	for _, cursor := range []string{"foreign:1", id.AuthorityID + ":01", id.AuthorityID + ":0"} {
		if _, err := api.GrantPage(cursor, 1); err == nil {
			t.Fatal("noncanonical recovery cursor accepted")
		}
	}
	page, err := api.GrantPage("", 1)
	if err != nil {
		t.Fatal(err)
	}
	page[0].Request.RequestID = "caller-only"
	got, err := api.GrantPage("", 1)
	if err != nil || got[0].Request.RequestID == "caller-only" {
		t.Fatal("caller mutated original grant")
	}
}

func TestGrantQueriesRecoverCommittedRequestAndCurrentReceipt(t *testing.T) {
	j, id, boot, path := journalFixture(t)
	r := issueRequest(id, boot, "query-request", 60)
	issued, err := j.Issue(r)
	if err != nil {
		t.Fatal(err)
	}
	if grant, err := j.Grant(issued.GrantID); err != nil || grant != issued {
		t.Fatalf("committed grant lookup: %+v/%v", grant, err)
	}
	if grant, err := j.LookupRequest(boot.NodeID, r.Binding.ClientID, r.RequestID); err != nil || grant != issued {
		t.Fatalf("lost-reply request lookup: %+v/%v", grant, err)
	}
	if _, err := j.LookupRequest(boot.NodeID, r.Binding.ClientID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing request: %v", err)
	}
	usage := Usage{RawUpload: 1, BilledBytes: 2, Remainder: 500000}
	if err := j.Report(Report{Binding: r.Binding, GrantID: issued.GrantID, Sequence: 1, Usage: usage}); err != nil {
		t.Fatal(err)
	}
	if err := j.CheckActiveGrant(issued.GrantID, boot); err != nil {
		t.Fatal(err)
	}
	if err := j.Report(Report{Binding: r.Binding, GrantID: issued.GrantID, Sequence: 2, Usage: usage, Seal: true}); err != nil {
		t.Fatal(err)
	}
	if err := j.CheckActiveGrant(issued.GrantID, boot); !errors.Is(err, ErrRequest) {
		t.Fatalf("sealed grant authorized renewal: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	grant, err := reopened.LookupRequest(boot.NodeID, r.Binding.ClientID, r.RequestID)
	if err != nil || !grant.Sealed || grant.Usage != usage || grant.ReportSequence != 2 || grant.Request != r {
		t.Fatalf("query lost immutable request/current receipt: %+v/%v", grant, err)
	}
}

func TestAccountLookupDistinguishesMissingFromCorruptAuthority(t *testing.T) {
	j, _, _, _ := journalFixture(t)
	if _, err := j.LookupAccount("missing-account"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account was not distinguished: %v", err)
	}
	accounts, err := j.AccountPage("", 1)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("initial account: %+v/%v", accounts, err)
	}
	id := accounts[0].Seed.ClientID
	if account, err := j.LookupAccount(id); err != nil || account != accounts[0] {
		t.Fatalf("existing account: %+v/%v", account, err)
	}
	if err := j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("accounts")).Put([]byte(id), []byte("{"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.LookupAccount(id); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("corrupt account was treated as unprovisioned: %v", err)
	}
}

func TestGrantRenewalQueryRejectsRetiredBootAndDeletedAccount(t *testing.T) {
	for _, action := range []string{"retire", "delete"} {
		t.Run(action, func(t *testing.T) {
			j, id, boot, _ := journalFixture(t)
			grant, err := j.Issue(issueRequest(id, boot, "active-request", 60))
			if err != nil {
				t.Fatal(err)
			}
			want := ErrIncarnation
			if action == "retire" {
				next := boot
				next.BootID = "fresh-boot-b"
				if err := j.RegisterBoot(next); err != nil {
					t.Fatal(err)
				}
			} else {
				want = ErrDeleted
				if err := j.Tombstone(grant.Request.Binding.ClientID); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.CheckActiveGrant(grant.GrantID, boot); !errors.Is(err, want) {
				t.Fatalf("%s grant authorized renewal: %v", action, err)
			}
			if retained, err := j.Grant(grant.GrantID); err != nil || retained != grant {
				t.Fatalf("query erased retained allocation: %+v/%v", retained, err)
			}
		})
	}
}

func TestGrantRenewalQueryRejectsSupersededPolicyAndWindow(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "policy", true: "window"}[reset], func(t *testing.T) {
			j, id, boot, _ := journalFixture(t)
			grant, err := j.Issue(issueRequest(id, boot, "before-change", 60))
			if err != nil {
				t.Fatal(err)
			}
			account, err := j.Account(grant.Request.Binding.ClientID)
			if err != nil {
				t.Fatal(err)
			}
			policy := account.Policy
			policy.Version++
			if reset {
				policy.WindowID = "next-window"
			} else {
				policy.QuotaBytes--
			}
			if _, err := j.ChangePolicy(ChangeRequest{Identity: id, ClientID: grant.Request.Binding.ClientID, RequestID: "change", ExpectedVersion: account.Policy.Version, Policy: policy, Reset: reset}); err != nil {
				t.Fatal(err)
			}
			if err := j.CheckActiveGrant(grant.GrantID, boot); !errors.Is(err, ErrRequest) {
				t.Fatalf("superseded grant authorized renewal: %v", err)
			}
			// Late cumulative receipts still settle the old reservation exactly.
			if err := j.Report(Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Usage: Usage{RawUpload: 1, BilledBytes: 1}, Seal: true}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccountPagesPreserveEveryInitialAndTerminalAccountAcrossBoundary(t *testing.T) {
	path, initial, _ := migrationFixture(t)
	seeds := make([]Seed, 1001)
	for i := range seeds {
		seeds[i] = initial[0]
		seeds[i].ClientID = fmt.Sprintf("client-%04d", i)
	}
	j, identity, err := CreateWithMigration(path, seeds, []string{seeds[1000].ClientID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	first, err := j.AccountPage("", 1000)
	if err != nil || len(first) != 1000 || first[0].Seed != seeds[0] || first[999].Seed != seeds[999] {
		t.Fatalf("first page dropped or changed initial accounts: %d/%v", len(first), err)
	}
	last, err := j.AccountPage(first[999].Seed.ClientID, 1000)
	if err != nil || len(last) != 1 || !last[0].Deleted || last[0].Seed != seeds[1000] {
		t.Fatalf("second page lost terminal history: %+v/%v", last, err)
	}
	empty, err := j.AccountPage(last[0].Seed.ClientID, 1000)
	if err != nil || len(empty) != 0 {
		t.Fatalf("last page was repeated: %+v/%v", empty, err)
	}
}
