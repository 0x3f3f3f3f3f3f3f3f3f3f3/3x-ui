package distribution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateBindsAllResourcesAndNeverOverwritesPriorManifest(t *testing.T) {
	root, original := manifestFixture(t)
	if err := os.Mkdir(filepath.Join(root, "licenses"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "licenses", "panel.txt"), []byte("license"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Generate(root, original.Target, original.SourceRevision, map[string]string{"go": "go1.27.1", "node": "v26.10.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 3 {
		t.Fatalf("lost a package resource: %+v", m.Files)
	}
	if _, err := VerifyFiles(root); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(root, original.Target, strings.Repeat("b", 40), m.Toolchains); err == nil {
		t.Fatal("overwrote an earlier manifest")
	}
	after, err := os.ReadFile(filepath.Join(root, ManifestName))
	if err != nil || string(before) != string(after) {
		t.Fatal("prior manifest changed")
	}
}

func TestGenerateRejectsDirtySourceWrongToolchainsAndRuntimeState(t *testing.T) {
	for _, mode := range []string{"source", "dirty", "go", "node", "target", "symlink", "database", "policy-state", "business-key", "missing-core"} {
		t.Run(mode, func(t *testing.T) {
			root, m := manifestFixture(t)
			toolchains := map[string]string{"go": "go1.27.1", "node": "v26.10.0"}
			switch mode {
			case "source":
				m.SourceRevision = "unknown"
			case "dirty":
				m.SourceRevision += "-dirty"
			case "go":
				toolchains["go"] = "go1.26.0"
			case "node":
				toolchains["node"] = "v22.0.0"
			case "target":
				m.Target.Arch = "unknown"
			case "symlink":
				if err := os.Symlink("x-ui", filepath.Join(root, "x-ui.sh")); err != nil {
					t.Fatal(err)
				}
			case "database":
				if err := os.WriteFile(filepath.Join(root, "x-ui.db"), []byte("state"), 0600); err != nil {
					t.Fatal(err)
				}
			case "policy-state":
				if err := os.WriteFile(filepath.Join(root, "ledger.json"), []byte("state"), 0600); err != nil {
					t.Fatal(err)
				}
			case "business-key":
				if err := os.WriteFile(filepath.Join(root, "ssh-host-key"), []byte("business key"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-core":
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(m.Files[1].Path))); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Generate(root, m.Target, m.SourceRevision, toolchains); err == nil {
				t.Fatalf("accepted %s package generation", mode)
			}
			if _, err := os.Lstat(filepath.Join(root, ManifestName)); !os.IsNotExist(err) {
				t.Fatal("left a generated manifest after rejected input")
			}
		})
	}
}
