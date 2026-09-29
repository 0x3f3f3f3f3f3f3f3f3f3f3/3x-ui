package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

func buildFixture(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"x-ui", "update-stage", "update.sh", "install.sh", "x-ui.sh", "x-ui.rc", "x-ui.service.debian", "x-ui.service.arch", "x-ui.service.rhel", "bin/xray-linux-amd64"} {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		mode := os.FileMode(0o755)
		if strings.Contains(name, ".service.") {
			mode = 0o644
		}
		if err := os.WriteFile(path, []byte(name), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir, []string{"--directory", dir, "--commit", strings.Repeat("c", 40), "--tag", "dev-latest", "--platform", "linux-amd64"}
}

func TestRunBuildsExclusiveManifest(t *testing.T) {
	dir, args := buildFixture(t)
	var out bytes.Buffer
	if err := run(t.Context(), args, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != filepath.Join(dir, "release.json")+"\n" {
		t.Fatalf("unexpected output %q", out.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest updatebundle.ReleaseManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Identity.Repository != updatebundle.ReleaseRepository || len(manifest.Files) != 10 {
		t.Fatalf("invalid manifest: %+v, %v", manifest, err)
	}
	out.Reset()
	if err := run(t.Context(), args, &out); err == nil || out.Len() != 0 {
		t.Fatalf("replacement accepted: %q, %v", out.String(), err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "release.json"))
	if string(after) != string(data) {
		t.Fatal("existing manifest changed")
	}
}

func TestRunRequiresCompleteBundleAndIdentity(t *testing.T) {
	for _, name := range []string{"missing-core", "missing-commit", "empty-arguments", "extra-argument", "unsupported-platform"} {
		t.Run(name, func(t *testing.T) {
			dir, args := buildFixture(t)
			switch name {
			case "missing-core":
				os.Remove(filepath.Join(dir, "bin/xray-linux-amd64"))
			case "missing-commit":
				args = append(args[:2], args[4:]...)
			case "empty-arguments":
				args = nil
			case "extra-argument":
				args = append(args, "unexpected")
			case "unsupported-platform":
				args[len(args)-1] = "linux-unknown"
			}
			var out bytes.Buffer
			if err := run(t.Context(), args, &out); err == nil || out.Len() != 0 {
				t.Fatalf("invalid build accepted: %q, %v", out.String(), err)
			}
			if _, err := os.Lstat(filepath.Join(dir, "release.json")); !os.IsNotExist(err) {
				t.Fatalf("invalid manifest published: %v", err)
			}
		})
	}
}
