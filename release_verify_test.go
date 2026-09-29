package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func copyReleaseFixtureFile(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("copy fixture: %v, %v", err, closeErr)
	}
}

func releaseRuntimeFixture(t *testing.T, panel, core string) (string, updatebundle.ReleaseIdentity) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, panel, "release-info").Output()
	if err != nil {
		t.Fatal(err)
	}
	var info config.ReleaseInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Commit) != 40 {
		t.Fatal("release fixture panel requires a full source stamp or VCS revision")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	copyReleaseFixtureFile(t, panel, filepath.Join(dir, "x-ui"))
	copyReleaseFixtureFile(t, core, filepath.Join(dir, "bin", xray.GetBinaryName()))
	for _, name := range []string{"update-stage", "update.sh", "install.sh", "x-ui.sh", "x-ui.rc", "x-ui.service.debian", "x-ui.service.arch", "x-ui.service.rhel"} {
		mode := os.FileMode(0o755)
		if strings.Contains(name, ".service.") {
			mode = 0o644
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture asset, not executed\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	identity := updatebundle.ReleaseIdentity{Repository: info.Repository, Commit: info.Commit, Tag: "fixture-runtime", Platform: info.Platform}
	manifest, err := updatebundle.BuildManifest(t.Context(), dir, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := updatebundle.WriteManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	return dir, identity
}

func TestVerifyReleaseCLIRequiresActualManagedCore(t *testing.T) {
	panel, managed, stock := os.Getenv("XUI_E2E_PANEL"), os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY"), os.Getenv("XUI_STOCK_XRAY_E2E_BINARY")
	if panel == "" || managed == "" || stock == "" {
		t.Skip("set actual panel, managed Xray and XUI_STOCK_XRAY_E2E_BINARY")
	}
	for _, tc := range []struct {
		name, core string
		wantOK     bool
	}{{"managed", managed, true}, {"stock", stock, false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir, identity := releaseRuntimeFixture(t, panel, tc.core)
			owned := t.TempDir()
			sentinel := filepath.Join(owned, "x-ui.db")
			if err := os.WriteFile(sentinel, []byte("keep database"), 0o600); err != nil {
				t.Fatal(err)
			}
			temporary := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, filepath.Join(dir, "x-ui"), "verify-release", "--directory", dir, "--commit", identity.Commit, "--tag", identity.Tag, "--platform", identity.Platform)
			cmd.Env = append(os.Environ(), "TMPDIR="+temporary, "XUI_DB_FOLDER="+owned, "XUI_DB_TYPE=sqlite", "XUI_DB_DSN=", "XUI_BIN_FOLDER="+filepath.Join(owned, "must-not-touch-bin"), "XUI_LOG_FOLDER="+filepath.Join(owned, "must-not-touch-log"))
			out, err := cmd.CombinedOutput()
			if tc.wantOK {
				if err != nil || !strings.Contains(string(out), "managed routing verified") {
					t.Fatalf("real managed core rejected: %v: %s", err, out)
				}
			} else if err == nil || !strings.Contains(string(out), "managed routing") {
				t.Fatalf("stock core not rejected by capability preflight: %v: %s", err, out)
			}
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "keep database" {
				t.Fatalf("business database changed: %q, %v", data, err)
			}
			entries, err := os.ReadDir(owned)
			if err != nil || len(entries) != 1 {
				t.Fatalf("business runtime folder changed: %v, %v", entries, err)
			}
			entries, err = os.ReadDir(temporary)
			if err != nil || len(entries) != 0 {
				t.Fatalf("probe temporary files remain: %v, %v", entries, err)
			}
		})
	}
}

func TestVerifyReleaseCLIRejectsBeforeLaunchingCore(t *testing.T) {
	panel := os.Getenv("XUI_E2E_PANEL")
	if panel == "" {
		t.Skip("set XUI_E2E_PANEL to a stamped or unmodified panel binary")
	}
	for _, name := range []string{"commit", "tag", "platform", "policy-ABI", "tampered-file", "foreign-panel", "extra-argument", "missing-commit"} {
		t.Run(name, func(t *testing.T) {
			owned := t.TempDir()
			core := filepath.Join(owned, "forbidden-core")
			if err := os.WriteFile(core, []byte("#!/bin/sh\nprintf 'started\\n' >> \"$XUI_FORBIDDEN_CORE_MARKER\"\nexit 19\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			dir, identity := releaseRuntimeFixture(t, panel, core)
			program := filepath.Join(dir, "x-ui")
			wantError := ""
			switch name {
			case "commit":
				identity.Commit = strings.Repeat("0", 40)
				wantError = "compiled panel source/platform"
			case "tag":
				identity.Tag = "other-tag"
				wantError = "release identity differs"
			case "platform":
				identity.Platform = "linux-unsupported"
				wantError = "compiled panel source/platform"
			case "policy-ABI":
				path := filepath.Join(dir, "release.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				os.WriteFile(path, []byte(strings.Replace(string(data), `"policyABI":1`, `"policyABI":0`, 1)), 0o644)
				wantError = "incompatible release schema"
			case "tampered-file":
				os.WriteFile(filepath.Join(dir, "update.sh"), []byte("tampered"), 0o755)
				wantError = "release file differs"
			case "foreign-panel":
				program = panel
				wantError = "must execute the panel inside"
			case "extra-argument", "missing-commit":
				wantError = "required:"
			}
			args := []string{"verify-release", "--directory", dir, "--commit", identity.Commit, "--tag", identity.Tag, "--platform", identity.Platform}
			if name == "extra-argument" {
				args = append(args, "unexpected")
			}
			if name == "missing-commit" {
				args = append(args[:3], args[5:]...)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, program, args...)
			marker := filepath.Join(owned, "core-started")
			cmd.Env = append(os.Environ(), "TMPDIR="+owned, "XUI_FORBIDDEN_CORE_MARKER="+marker)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), wantError) {
				t.Errorf("wrong rejection: %v: %s; want %q", err, out, wantError)
			}
			if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Errorf("invalid bundle launched the core: %v", err)
			}
			entries, err := os.ReadDir(owned)
			if err != nil || len(entries) != 1 {
				t.Fatalf("invalid bundle created runtime files: %v, %v", entries, err)
			}
		})
	}
}
