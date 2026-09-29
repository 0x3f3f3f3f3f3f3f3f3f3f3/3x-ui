package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

func TestReleaseInfoCLIHasNoDatabaseSideEffects(t *testing.T) {
	binary := os.Getenv("XUI_E2E_PANEL")
	if binary == "" {
		t.Skip("set XUI_E2E_PANEL to a normal panel binary")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "x-ui.db")
	sentinel := []byte("owned database sentinel; must not be opened as SQLite")
	if err := os.WriteFile(db, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "release-info")
	cmd.Env = append(os.Environ(), "XUI_DB_FOLDER="+dir, "XUI_DB_TYPE=sqlite", "XUI_DB_DSN=", "XUI_BIN_FOLDER="+filepath.Join(dir, "must-not-create-bin"), "XUI_LOG_FOLDER="+filepath.Join(dir, "must-not-create-log"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release-info failed: %v: %s", err, out)
	}
	var info struct {
		Repository   string `json:"repository"`
		Commit       string `json:"commit"`
		Platform     string `json:"platform"`
		PanelVersion string `json:"panelVersion"`
		PolicyABI    int    `json:"policyABI"`
		RoutingABI   int    `json:"routingABI"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		t.Fatalf("release-info did not return standalone JSON: %q, %v", out, err)
	}
	if info.Repository != updatebundle.ReleaseRepository || len(info.Commit) != 40 || info.PolicyABI != 1 || info.RoutingABI != 1 || info.PanelVersion == "" {
		t.Fatalf("incomplete compiled release metadata: %+v", info)
	}
	if runtime.GOARCH != "arm" && info.Platform != runtime.GOOS+"-"+runtime.GOARCH {
		t.Fatalf("wrong native platform: %+v", info)
	}
	if expected := os.Getenv("XUI_E2E_RELEASE_COMMIT"); expected != "" && info.Commit != expected {
		t.Fatalf("commit %q, want compiled %q", info.Commit, expected)
	}
	version := exec.CommandContext(ctx, binary, "-v")
	version.Env = cmd.Env
	gotVersion, err := version.CombinedOutput()
	if err != nil || strings.TrimSpace(string(gotVersion)) != info.PanelVersion {
		t.Fatalf("ordinary version behavior changed: %q, %v", gotVersion, err)
	}
	got, err := os.ReadFile(db)
	if err != nil || !bytes.Equal(got, sentinel) {
		t.Fatalf("database touched: %q, %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("release-info created runtime files: %v, %v", entries, err)
	}
}

func TestReleaseInfoCLIRejectsArguments(t *testing.T) {
	binary := os.Getenv("XUI_E2E_PANEL")
	if binary == "" {
		t.Skip("set XUI_E2E_PANEL to a normal panel binary")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "release-info", "unexpected")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("invalid release-info arguments succeeded: %q", out)
	}
}
