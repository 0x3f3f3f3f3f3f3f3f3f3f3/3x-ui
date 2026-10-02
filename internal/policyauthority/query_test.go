package policyauthority

import (
	"fmt"
	"testing"
)

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
