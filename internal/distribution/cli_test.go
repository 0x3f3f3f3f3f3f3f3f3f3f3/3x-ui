package distribution

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIncomingCLIRejectsMutableGeodataInNewPackage(t *testing.T) {
	root := t.TempDir()
	correctionPackage(t, root, strings.Repeat("a", 40), map[string]string{"bin/geoip.dat": "original geodata"})
	if err := os.WriteFile(filepath.Join(root, "bin/geoip.dat"), []byte("modified geodata"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunCommand(context.Background(), []string{"verify", root}, &output); err != nil {
		t.Fatal(err)
	}
	if err := RunCommand(context.Background(), []string{"verify-incoming", root}, &output); err == nil {
		t.Fatal("CLI accepted changed incoming geodata")
	}
}

func TestPackageCommandInfoAndFailedVerifyLeaveConfiguredStateUntouched(t *testing.T) {
	root := t.TempDir()
	key, state := filepath.Join(root, "business-key"), filepath.Join(root, "accounting-state")
	for _, name := range []string{key, state} {
		if err := os.WriteFile(name, []byte("preserved"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XUI_DB_DSN", "unreachable-configured-database")
	t.Setenv("XUI_NODE_TOKEN_KEY_FILE", key)
	t.Setenv("XUI_CUSTOM_CORE_POLICY_STATE_DIR", state)
	var output bytes.Buffer
	if err := RunCommand(context.Background(), []string{"info"}, &output); err != nil {
		t.Fatal(err)
	}
	var report BinaryReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Target != CurrentTarget() || report.Compatibility != "traffic-control-v1" {
		t.Fatalf("bad panel report: %+v", report)
	}
	if err := RunCommand(context.Background(), []string{"verify", root}, &output); err == nil {
		t.Fatal("missing package accepted")
	}
	for _, name := range []string{key, state} {
		b, err := os.ReadFile(name)
		if err != nil || string(b) != "preserved" {
			t.Fatalf("changed state: %s %v", name, err)
		}
	}
	for _, args := range [][]string{nil, {"info", "extra"}, {"verify"}, {"unknown"}} {
		if err := RunCommand(context.Background(), args, &output); err == nil {
			t.Fatalf("accepted invalid command %v", args)
		}
	}
}

func TestPackageStageExtractsBeforeValidationFailureAndPreservesOldTree(t *testing.T) {
	archive := testArchive(t, []*tar.Header{{Name: "x-ui/x-ui", Typeflag: tar.TypeReg, Mode: 0755, Size: 4}}, [][]byte{[]byte("test")})
	parent := t.TempDir()
	old := filepath.Join(parent, "old-installation")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(old, "accounting-state")
	if err := os.WriteFile(sentinel, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "new-staging")
	var output bytes.Buffer
	err := RunCommand(context.Background(), []string{"stage", archive, destination}, &output)
	if err == nil {
		t.Fatal("stage accepted a package without a manifest")
	}
	if _, statErr := os.Stat(filepath.Join(destination, "x-ui", "x-ui")); statErr != nil {
		t.Fatalf("stage command was not executed: %v; %v", statErr, err)
	}
	after, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(after) != "retained" {
		t.Fatal("stage changed an installed tree")
	}
}
