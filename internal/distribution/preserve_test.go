package distribution

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreserveResourcesKeepsBusinessStateAndCustomFilesWithoutReplacingPair(t *testing.T) {
	old := t.TempDir()
	candidate, m := manifestFixture(t)
	fixtures := map[string]string{"x-ui": "old executable", "bin/" + CoreBinaryName(m.Target.OS, m.Target.Arch): "old core", "bin/custom.dat": "custom resources", "bin/config.json": "runtime config", "data/core-ledger": "monotonic business state", "data/business-ssh-host-key": "independent business key", "data/x-ui.db": "database bytes"}
	for name, content := range fixtures {
		p := filepath.Join(old, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("custom.dat", filepath.Join(old, "bin", "custom-link")); err != nil {
		t.Fatal(err)
	}
	if err := PreserveResources(old, candidate, &m); err != nil {
		t.Fatal(err)
	}
	for name, content := range fixtures {
		b, err := os.ReadFile(filepath.Join(candidate, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		switch name {
		case "x-ui":
			if string(b) != "panel bytes" {
				t.Fatal("old panel replaced the candidate")
			}
		case m.Files[1].Path:
			if string(b) != "core bytes" {
				t.Fatal("old core replaced the candidate")
			}
		default:
			if string(b) != content {
				t.Fatalf("lost %s", name)
			}
		}
		previous, err := os.ReadFile(filepath.Join(old, filepath.FromSlash(name)))
		if err != nil || string(previous) != content {
			t.Fatalf("old resource changed: %s", name)
		}
	}
	link, err := os.Readlink(filepath.Join(candidate, "bin", "custom-link"))
	if err != nil || link != "custom.dat" {
		t.Fatal("custom resource link was not preserved as a link")
	}
	if _, err := VerifyFiles(old); err == nil {
		t.Fatal("fixture unexpectedly has a package manifest")
	}
}

func TestPreserveResourcesRefusesCollidingUnknownResources(t *testing.T) {
	old := t.TempDir()
	candidate, m := manifestFixture(t)
	for _, root := range []string{old, candidate} {
		if err := os.WriteFile(filepath.Join(root, "custom-resource"), []byte(root), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := PreserveResources(old, candidate, &m); err == nil {
		t.Fatal("an undeclared candidate file was overwritten")
	}
	oldBytes, err := os.ReadFile(filepath.Join(old, "custom-resource"))
	if err != nil || string(oldBytes) != old {
		t.Fatal("old resources changed after rejection")
	}
}
