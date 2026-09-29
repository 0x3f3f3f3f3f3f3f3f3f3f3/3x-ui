package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareUpdateCLIUsesInstalledSourceWithoutDatabase(t *testing.T) {
	panel := os.Getenv("XUI_E2E_PANEL")
	if panel == "" {
		t.Skip("set XUI_E2E_PANEL to a normal panel binary")
	}
	for _, mode := range []string{"valid", "changed-script", "extra-argument"} {
		t.Run(mode, func(t *testing.T) {
			owned := t.TempDir()
			core := filepath.Join(owned, "forbidden-core")
			if err := os.WriteFile(core, []byte("#!/bin/sh\nprintf invoked >\"$XUI_FORBIDDEN_CORE_MARKER\"\nexit 19\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			dir, _ := releaseRuntimeFixture(t, panel, core)
			if err := os.WriteFile(filepath.Join(dir, "bin", "runtime.json"), []byte("existing runtime configuration"), 0o600); err != nil {
				t.Fatal(err)
			}
			if mode == "changed-script" {
				if err := os.WriteFile(filepath.Join(dir, "update.sh"), []byte("tampered updater"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			dbdir, temporary := t.TempDir(), t.TempDir()
			sentinel := []byte("database sentinel, must not be opened or migrated")
			if err := os.WriteFile(filepath.Join(dbdir, "x-ui.db"), sentinel, 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"prepare-update"}
			if mode == "extra-argument" {
				args = append(args, "unexpected")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, filepath.Join(dir, "x-ui"), args...)
			cmd.Env = append(os.Environ(), "TMPDIR="+temporary, "XUI_DB_FOLDER="+dbdir, "XUI_DB_TYPE=sqlite", "XUI_DB_DSN=", "XUI_FORBIDDEN_CORE_MARKER="+filepath.Join(owned, "core-invoked"))
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if mode == "valid" {
				if err != nil {
					t.Fatalf("prepare-update failed: %v %s", err, stderr.String())
				}
				prepared := strings.TrimSuffix(string(output), "\n")
				if filepath.Dir(prepared) != temporary {
					t.Fatalf("command did not return a private copied script: %q", output)
				}
				data, err := os.ReadFile(prepared)
				if err != nil || string(data) != "fixture asset, not executed\n" {
					t.Fatalf("wrong updater copied: %q %v", data, err)
				}
				if err := os.Remove(prepared); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || len(output) != 0 {
				t.Fatalf("invalid command or script accepted: %q %v", output, err)
			}
			data, err := os.ReadFile(filepath.Join(dbdir, "x-ui.db"))
			if err != nil || !bytes.Equal(data, sentinel) {
				t.Fatalf("database touched: %q %v", data, err)
			}
			for _, folder := range []string{dbdir, temporary} {
				entries, err := os.ReadDir(folder)
				want := 0
				if folder == dbdir {
					want = 1
				}
				if err != nil || len(entries) != want {
					t.Fatalf("unexpected runtime/temp files: %v %v", entries, err)
				}
			}
			if _, err := os.Stat(filepath.Join(owned, "core-invoked")); !os.IsNotExist(err) {
				t.Fatalf("preparation started a core: %v", err)
			}
		})
	}
}
