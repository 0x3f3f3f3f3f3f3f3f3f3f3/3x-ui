package distribution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func manifestFixture(t *testing.T) (string, Manifest) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	m := Manifest{FormatVersion: 1, SourceRevision: strings.Repeat("a", 40), Compatibility: "traffic-control-v1", Target: Target{OS: runtime.GOOS, Arch: runtime.GOARCH}, RequiredCapabilities: RequiredCapabilities(), Toolchains: map[string]string{"go": GoToolchain, "node": NodeToolchain}}
	for _, tc := range []struct{ path, role, content string }{{"x-ui", "panel", "panel bytes"}, {"bin/" + CoreBinaryName(runtime.GOOS, runtime.GOARCH), "core", "core bytes"}} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(tc.path)), []byte(tc.content), 0700); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(tc.content))
		m.Files = append(m.Files, File{Path: tc.path, Role: tc.role, Size: int64(len(tc.content)), SHA256: hex.EncodeToString(h[:])})
	}
	return root, m
}

func writeManifestFixture(t *testing.T, root string, m Manifest) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestName), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyFilesBindsPanelCoreAndDetectsTampering(t *testing.T) {
	root, m := manifestFixture(t)
	writeManifestFixture(t, root, m)
	if _, err := VerifyFiles(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x-ui"), []byte("wrong bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFiles(root); err == nil {
		t.Fatal("changed panel executable was accepted")
	}
}

func TestVerifyFilesRejectsUnsafeAndIncompletePairs(t *testing.T) {
	for _, kind := range []string{"source", "compatibility", "missing-toolchains", "wrong-toolchains", "wrong-target", "missing-native", "missing-core", "duplicate-core", "duplicate-path", "absolute", "traversal", "unnormalized", "wrong-core-name", "symlink", "parent-symlink", "size", "hash", "missing-file"} {
		t.Run(kind, func(t *testing.T) {
			root, m := manifestFixture(t)
			switch kind {
			case "source":
				m.SourceRevision = "unknown"
			case "compatibility":
				m.Compatibility = "official-xray"
			case "missing-toolchains":
				m.Toolchains = nil
			case "wrong-toolchains":
				m.Toolchains["go"] = "go1.26.0"
			case "wrong-target":
				m.Target.OS = "different-os"
			case "missing-native":
				m.RequiredCapabilities = []string{"fixed-point-billing-v1"}
			case "missing-core":
				m.Files = m.Files[:1]
			case "duplicate-core":
				m.Files = append(m.Files, m.Files[1])
				m.Files[2].Path = "another-core"
			case "duplicate-path":
				m.Files = append(m.Files, m.Files[0])
			case "absolute":
				m.Files[0].Path = "/x-ui"
			case "traversal":
				m.Files[0].Path = "../x-ui"
			case "unnormalized":
				m.Files[0].Path = "bin/../x-ui"
			case "wrong-core-name":
				m.Files[1].Path = "bin/official-xray"
			case "symlink":
				if err := os.Rename(filepath.Join(root, "x-ui"), filepath.Join(root, "old-panel")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("old-panel", filepath.Join(root, "x-ui")); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				if err := os.Rename(filepath.Join(root, "bin"), filepath.Join(root, "old-bin")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("old-bin", filepath.Join(root, "bin")); err != nil {
					t.Fatal(err)
				}
			case "size":
				m.Files[0].Size++
			case "hash":
				m.Files[0].SHA256 = strings.Repeat("0", 64)
			case "missing-file":
				if err := os.Remove(filepath.Join(root, "x-ui")); err != nil {
					t.Fatal(err)
				}
			}
			writeManifestFixture(t, root, m)
			if _, err := VerifyFiles(root); err == nil {
				t.Fatalf("accepted %s candidate", kind)
			}
		})
	}
}

func TestVerifyFilesRejectsAmbiguousAndUnboundedManifest(t *testing.T) {
	for _, kind := range []string{"unknown-field", "duplicate-key", "duplicate-nested-key", "second-object", "oversize", "manifest-link"} {
		t.Run(kind, func(t *testing.T) {
			root, m := manifestFixture(t)
			writeManifestFixture(t, root, m)
			p := filepath.Join(root, ManifestName)
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unknown-field":
				b = append([]byte(`{"unexpected":true,`), b[1:]...)
			case "duplicate-key":
				b = append([]byte(`{"sourceRevision":"wrong",`), b[1:]...)
			case "duplicate-nested-key":
				b = []byte(strings.Replace(string(b), `"os":`, `"os":"wrong","os":`, 1))
			case "second-object":
				b = append(b, []byte(` {}`)...)
			case "oversize":
				b = []byte(strings.Repeat(" ", 2*1024*1024))
			case "manifest-link":
				if err := os.Rename(p, p+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(ManifestName+".original", p); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "manifest-link" {
				if err := os.WriteFile(p, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := VerifyFiles(root); err == nil {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
}
