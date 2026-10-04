package policyauthority

import (
	"fmt"
	"testing"
)

func TestManagedAccountPagesReadOriginalParentAndAtomicBalances(t *testing.T) {
	j, path := managedJournalFixture(t, nil, `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`)
	if err := j.ActivateManagedCoordinator("managed-source"); err != nil {
		t.Fatal(err)
	}
	seed, global := managedOriginFixture()
	if err := j.AddManagedAccount(seed, global); err != nil {
		t.Fatal(err)
	}
	parent := "33333333-3333-4333-8333-333333333333"
	for i := 1; i <= 5; i++ {
		origin := global
		origin.ClientID = fmt.Sprintf("44444444-4444-4444-8444-%012d", i)
		origin.ParentClientID, origin.Scope = parent, "node"
		origin.NodeID, origin.SourceID = fmt.Sprintf("node-%d", i), fmt.Sprintf("source-%d", i)
		local := seed
		local.ClientID = origin.ClientID
		if err := j.AddManagedAccount(local, origin); err != nil {
			t.Fatal(err)
		}
	}
	globalPage, err := j.ManagedAccountPage(global.ParentClientID, "", 2)
	if err != nil || len(globalPage) != 1 || globalPage[0].Origin != global || globalPage[0].Account.Seed != seed {
		t.Fatalf("global page: %+v/%v", globalPage, err)
	}
	var after string
	count := 0
	for {
		page, err := j.ManagedAccountPage(parent, after, 2)
		if err != nil || len(page) > 2 {
			t.Fatalf("bounded page: %+v/%v", page, err)
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			count++
			if row.Origin.NodeID != fmt.Sprintf("node-%d", count) || row.Origin.ParentClientID != parent || row.Account.Seed.ClientID != row.Origin.ClientID || row.Account.HeldCapacity != 0 {
				t.Fatalf("cross-parent or incoherent page: %+v", row)
			}
		}
		after = page[len(page)-1].Origin.NodeID
	}
	if count != 5 {
		t.Fatalf("lost page rows: %d", count)
	}
	for _, limit := range []int{0, 129} {
		if _, err := j.ManagedAccountPage(parent, "", limit); err == nil {
			t.Fatal("invalid page bound admitted")
		}
	}
	if _, err := j.ManagedAccountPage("invalid", "", 2); err == nil {
		t.Fatal("invalid parent admitted")
	}
	if _, err := j.ManagedAccountPage(parent, "\x00", 2); err == nil {
		t.Fatal("invalid cursor admitted")
	}
	if _, err := j.ManagedAccountPage(global.ParentClientID, "node-1", 2); err == nil {
		t.Fatal("node cursor admitted for global parent")
	}
	missing, err := j.ManagedAccountPage("55555555-5555-4555-8555-555555555555", "", 2)
	if err != nil || len(missing) != 0 {
		t.Fatal("missing origin fabricated account", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, j.Identity())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	page, err := reopened.ManagedAccountPage(parent, "node-4", 2)
	if err != nil || len(page) != 1 || page[0].Origin.NodeID != "node-5" {
		t.Fatal("original page did not survive reopen", err)
	}
}
