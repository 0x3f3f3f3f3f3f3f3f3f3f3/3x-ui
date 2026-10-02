package service

import (
	"bytes"
	"errors"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestDatabaseRestoreReopenFailureKeepsRuntimeStopped(t *testing.T) {
	for _, kind := range []string{"sqlite", "postgres-dump", "sqlite-to-postgres"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "postgres-dump" && runtime.GOOS == "windows" {
				t.Skip("the isolated pg_restore executable uses POSIX shell")
			}
			t.Setenv("XUI_DB_TYPE", "sqlite")
			t.Setenv("XUI_DB_DSN", "")
			setupPolicyLedgerDB(t)
			incoming := filepath.Join(t.TempDir(), "incoming.db")
			if err := database.BackupSQLite(incoming); err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(incoming)
			if err != nil {
				t.Fatal(err)
			}
			oldStop, oldRestart, oldReopen := stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore, reopenDatabaseAfterRestore
			var restarts int
			stopXrayBeforeDatabaseRestore = func(*ServerService) error { return nil }
			restartXrayAfterDatabaseRestore = func(*ServerService, *databaseRestoreOwner) error { restarts++; return nil }
			reopenDatabaseAfterRestore = func(string) error { return errors.New("injected database reopening failure") }
			t.Cleanup(func() {
				stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore, reopenDatabaseAfterRestore = oldStop, oldRestart, oldReopen
				if err := database.InitDB(config.GetDBPath()); err != nil {
					t.Errorf("reopen retained test database: %v", err)
				}
			})
			svc := &ServerService{}
			var restore func(multipart.File) error
			switch kind {
			case "sqlite":
				restore = func(f multipart.File) error { return svc.ImportDB(f, false) }
			case "sqlite-to-postgres":
				// The migration fails before contacting a server. This case tests
				// error activation only, not PostgreSQL data integration.
				t.Setenv("XUI_DB_DSN", "not a PostgreSQL DSN")
				restore = func(f multipart.File) error { return svc.migrateSQLiteIntoPostgres(f, false) }
			case "postgres-dump":
				binDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(binDir, "pg_restore"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("XUI_DB_DSN", "postgresql://test@example.invalid/unused")
				payload = []byte("PGDMPisolated restore fixture")
				restore = func(f multipart.File) error { return svc.restorePostgresDump(f, false) }
			}
			if err := restore(restoreUpload{bytes.NewReader(payload)}); err == nil || !strings.Contains(err.Error(), "injected database reopening failure") {
				t.Errorf("restore did not expose the failed reopening: %v", err)
			}
			if restarts != 0 {
				t.Errorf("restore tried to activate business runtime %d times without a successfully reopened database", restarts)
			}
		})
	}
}

func TestDatabaseRestoreReopenedFallbackCanRestart(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	setupPolicyLedgerDB(t)
	if err := database.GetDB().Create(&model.Setting{Key: "fallback-marker", Value: "original"}).Error; err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(t.TempDir(), "incoming.db")
	if err := database.BackupSQLite(incoming); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(incoming)
	if err != nil {
		t.Fatal(err)
	}
	oldStop, oldRestart, oldReopen := stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore, reopenDatabaseAfterRestore
	var restarts, reopens int
	stopXrayBeforeDatabaseRestore = func(*ServerService) error { return nil }
	restartXrayAfterDatabaseRestore = func(_ *ServerService, owner *databaseRestoreOwner) error {
		var marker model.Setting
		if err := owner.currentDatabase().First(&marker, "key = ?", "fallback-marker").Error; err != nil || marker.Value != "original" {
			t.Errorf("fallback runtime activation without original usable data: %+v/%v", marker, err)
		}
		restarts++
		return nil
	}
	reopenDatabaseAfterRestore = func(path string) error {
		reopens++
		if reopens == 1 {
			return errors.New("injected candidate migration failure")
		}
		return database.InitDB(path)
	}
	t.Cleanup(func() {
		stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore, reopenDatabaseAfterRestore = oldStop, oldRestart, oldReopen
	})
	if err := (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(payload)}, false); err == nil || !strings.Contains(err.Error(), "injected candidate migration failure") {
		t.Fatalf("candidate failure was lost: %v", err)
	}
	if reopens != 2 || restarts != 1 {
		t.Fatalf("known usable fallback was not recovered once: reopens=%d restarts=%d", reopens, restarts)
	}
}
