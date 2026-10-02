package distribution

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRollbackCopiesOldCodeAndOnlyCurrentBusinessState(t *testing.T) {
	previous := t.TempDir()
	candidate := t.TempDir()
	current, m := manifestFixture(t)
	for name, data := range map[string]string{"x-ui": "old code", "x-ui.db": "stale spent=5", "deleted-user.key": "stale credential"} {
		if err := os.WriteFile(filepath.Join(previous, name), []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(previous, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	core := filepath.Join("bin", CoreBinaryName(m.Target.OS, m.Target.Arch))
	if err := os.WriteFile(filepath.Join(previous, core), []byte("old core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "x-ui.db"), []byte("current spent=900"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyRollbackCode(previous, candidate); err != nil {
		t.Fatal(err)
	}
	if err := PreserveResources(current, candidate, &m); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"x-ui": "old code", core: "old core", "x-ui.db": "current spent=900"} {
		got, err := os.ReadFile(filepath.Join(candidate, name))
		if err != nil || string(got) != want {
			t.Fatal(name, string(got), err)
		}
	}
	if _, err := os.Stat(filepath.Join(candidate, "deleted-user.key")); !os.IsNotExist(err) {
		t.Fatal("resurrected stale credential", err)
	}
	oldDB, _ := os.ReadFile(filepath.Join(previous, "x-ui.db"))
	if string(oldDB) != "stale spent=5" {
		t.Fatal("previous receipt was modified")
	}
}
