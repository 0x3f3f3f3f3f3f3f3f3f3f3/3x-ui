package service

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

func TestDatabaseRestoreKeepsUnrelatedTemporaryFile(t *testing.T) {
	testDatabaseRestoreKeepsUnrelatedFile(t, ".temp")
}

func TestDatabaseRestoreKeepsPreviousFallbackArtifact(t *testing.T) {
	testDatabaseRestoreKeepsUnrelatedFile(t, ".backup")
}

func testDatabaseRestoreKeepsUnrelatedFile(t *testing.T, suffix string) {
	t.Helper()
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	setupPolicyLedgerDB(t)
	input := filepath.Join(t.TempDir(), "incoming.db")
	if err := database.BackupSQLite(input); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	path := config.GetDBPath() + suffix
	sentinel := []byte("unrelated prior engineering or recovery evidence")
	if err := os.WriteFile(path, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	oldStop, oldRestart := stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore
	stopXrayBeforeDatabaseRestore = func(*ServerService) error { return nil }
	restartXrayAfterDatabaseRestore = func(*ServerService, *databaseRestoreOwner) error { return nil }
	t.Cleanup(func() { stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore = oldStop, oldRestart })
	if err := (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(payload)}, false); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, sentinel) {
		t.Fatalf("restore removed or changed an unrelated temporary file: %v", err)
	}
}
