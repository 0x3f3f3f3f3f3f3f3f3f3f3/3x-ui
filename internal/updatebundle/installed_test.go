package updatebundle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareInstalledUpdaterAllowsRuntimeFilesAndSurvivesReplacement(t *testing.T) {
	directory, identity := manifestFixture(t, "linux-arm64")
	manifest, err := BuildManifest(t.Context(), directory, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(directory, manifest); err != nil {
		t.Fatal(err)
	}
	runtimeFile := filepath.Join(directory, "bin", "runtime-config.json")
	if err := os.WriteFile(runtimeFile, []byte("owned runtime secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(directory, "runtime-certs")); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	script, err := PrepareInstalledUpdater(t.Context(), directory, parent, identity.Commit, identity.Platform)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(script) != parent {
		t.Fatalf("copy is not outside installation: %s", script)
	}
	st, err := os.Stat(script)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("updater permissions: %v %v", st, err)
	}
	if data, err := os.ReadFile(runtimeFile); err != nil || string(data) != "owned runtime secret" {
		t.Fatalf("runtime state changed: %s %v", data, err)
	}
	moved := directory + "-previous"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	got, err := os.ReadFile(script)
	if err != nil || !bytes.Equal(got, []byte("fixture:update.sh")) {
		t.Fatalf("bootstrap lost during replacement: %s %v", got, err)
	}
}

func TestPrepareInstalledUpdaterRejectsUnverifiedInstalledScript(t *testing.T) {
	for _, mode := range []string{"missing-manifest", "manifest-link", "script-link", "script-missing", "script-changed", "script-nonexecutable", "script-too-large", "source", "platform", "unknown-source", "invalid-tag", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			directory, identity := manifestFixture(t, "linux-arm64")
			manifest, err := BuildManifest(t.Context(), directory, identity)
			if err != nil {
				t.Fatal(err)
			}
			if err := WriteManifest(directory, manifest); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(directory, "update.sh")
			commit, platform := identity.Commit, identity.Platform
			switch mode {
			case "missing-manifest":
				if err := os.Remove(filepath.Join(directory, ManifestName)); err != nil {
					t.Fatal(err)
				}
			case "manifest-link", "script-link":
				path := script
				if mode == "manifest-link" {
					path = filepath.Join(directory, ManifestName)
				}
				external := filepath.Join(t.TempDir(), "original")
				if err := os.Rename(path, external); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, path); err != nil {
					t.Fatal(err)
				}
			case "script-missing":
				if err := os.Remove(script); err != nil {
					t.Fatal(err)
				}
			case "script-changed":
				if err := os.WriteFile(script, []byte("changed:update.sh"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "script-nonexecutable":
				if err := os.Chmod(script, 0o644); err != nil {
					t.Fatal(err)
				}
			case "script-too-large":
				data := bytes.Repeat([]byte{'x'}, (2<<20)+1)
				if err := os.WriteFile(script, data, 0o755); err != nil {
					t.Fatal(err)
				}
				manifest.Files["update.sh"] = ReleaseFile{SHA256: digest(data), Size: int64(len(data)), Executable: true}
				writeFixtureManifest(t, directory, manifest)
			case "source":
				commit = strings.Repeat("c", 40)
			case "platform":
				platform = "linux-amd64"
			case "unknown-source":
				commit = ""
			case "invalid-tag":
				manifest.Identity.Tag = "../../main"
				writeFixtureManifest(t, directory, manifest)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			parent := t.TempDir()
			path, err := PrepareInstalledUpdater(ctx, directory, parent, commit, platform)
			if err == nil || path != "" {
				t.Fatalf("unverified script published: %s %v", path, err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial copy leaked: %v %v", entries, err)
			}
		})
	}
}

func TestPrepareInstalledUpdaterRejectsParentInsideInstallation(t *testing.T) {
	directory, identity := manifestFixture(t, "linux-arm64")
	manifest, err := BuildManifest(t.Context(), directory, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(directory, manifest); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(directory, "temp")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(inside, alias); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{directory, inside, alias} {
		path, err := PrepareInstalledUpdater(t.Context(), directory, parent, identity.Commit, identity.Platform)
		if err == nil || path != "" {
			t.Fatalf("bootstrap can disappear during activation: %s %v", path, err)
		}
	}
}
