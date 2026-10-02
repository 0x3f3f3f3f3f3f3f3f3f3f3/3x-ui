package service

import (
	"bytes"
	"database/sql"
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
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type restoreUpload struct{ *bytes.Reader }

func (restoreUpload) Close() error { return nil }

func TestDatabaseRestoreAcceptsConfirmedAbsentCore(t *testing.T) {
	for _, kind := range []string{"missing", "not-started"} {
		t.Run(kind, func(t *testing.T) {
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
			previous := currentXrayProcess()
			manuallyStopped := isManuallyStopped.Load()
			xrayState.replace(nil)
			if kind == "not-started" {
				xrayState.replace(xray.NewTestProcess(&xray.Config{}, filepath.Join(t.TempDir(), "never-started.json")))
			}
			t.Cleanup(func() { xrayState.replace(previous); isManuallyStopped.Store(manuallyStopped) })
			oldRestart := restartXrayAfterDatabaseRestore
			var restarts int
			restartXrayAfterDatabaseRestore = func(*ServerService, *databaseRestoreOwner) error { restarts++; return nil }
			t.Cleanup(func() { restartXrayAfterDatabaseRestore = oldRestart })
			if err := (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(payload)}, false); err != nil {
				t.Fatalf("confirmed absent core blocked database import: %v", err)
			}
			if restarts != 1 {
				t.Fatalf("completed import attempted %d runtime activations, want 1", restarts)
			}
		})
	}
}

func TestDatabaseRestoreStopFailureLeavesDatabaseAndRuntimeUntouched(t *testing.T) {
	for _, kind := range []string{"sqlite", "postgres-dump", "sqlite-to-postgres"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "postgres-dump" && runtime.GOOS == "windows" {
				t.Skip("the fault-injection pg_restore fixture uses a POSIX executable")
			}
			t.Setenv("XUI_DB_TYPE", "sqlite")
			t.Setenv("XUI_DB_DSN", "")
			setupPolicyLedgerDB(t)
			if err := database.GetDB().Create(&model.Setting{Key: "restore-stop-marker", Value: "current"}).Error; err != nil {
				t.Fatal(err)
			}
			originalDB := database.GetDB()
			originalPool, err := originalDB.DB()
			if err != nil {
				t.Fatal(err)
			}
			incoming := filepath.Join(t.TempDir(), "incoming.db")
			if err := database.BackupSQLite(incoming); err != nil {
				t.Fatal(err)
			}
			input, err := sql.Open("sqlite3", incoming)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := input.Exec("UPDATE settings SET value = 'imported' WHERE key = 'restore-stop-marker'"); err != nil {
				t.Fatal(err)
			}
			if err := input.Close(); err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(incoming)
			if err != nil {
				t.Fatal(err)
			}
			oldStop, oldRestart := stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore
			var stops, restarts int
			stopXrayBeforeDatabaseRestore = func(*ServerService) error {
				stops++
				return errors.New("injected uncertain core termination")
			}
			restartXrayAfterDatabaseRestore = func(*ServerService, *databaseRestoreOwner) error { restarts++; return nil }
			t.Cleanup(func() { stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore = oldStop, oldRestart })
			svc := &ServerService{}
			var restore func(multipart.File) error
			invoked := filepath.Join(t.TempDir(), "restore-invoked")
			switch kind {
			case "sqlite":
				restore = func(f multipart.File) error { return svc.ImportDB(f, false) }
			case "sqlite-to-postgres":
				restore = func(f multipart.File) error { return svc.migrateSQLiteIntoPostgres(f, false) }
			case "postgres-dump":
				binDir := t.TempDir()
				t.Setenv("RESTORE_TEST_INVOKED", invoked)
				// Validation succeeds; an attempted destructive restore is recorded
				// and rejected without connecting to any PostgreSQL server.
				stub := "#!/bin/sh\ncase \"$1\" in --list) exit 0;; esac\nprintf attempted > \"$RESTORE_TEST_INVOKED\"\nexit 1\n"
				if err := os.WriteFile(filepath.Join(binDir, "pg_restore"), []byte(stub), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("XUI_DB_DSN", "postgresql://test@example.invalid/unused")
				payload = []byte("PGDMPtest validated by isolated executable")
				restore = func(f multipart.File) error { return svc.restorePostgresDump(f, false) }
			}
			err = restore(restoreUpload{bytes.NewReader(payload)})
			if err == nil || !strings.Contains(err.Error(), "injected uncertain core termination") {
				t.Errorf("restore did not report the failed stop: %v", err)
			}
			if stops != 1 || restarts != 0 {
				t.Errorf("failed-stop lifecycle: stops=%d restarts=%d, want 1 and 0", stops, restarts)
			}
			if database.GetDB() != originalDB || originalPool.Ping() != nil {
				t.Error("failed core stop closed or replaced the original database pool")
			}
			var marker model.Setting
			if err := database.GetDB().Where("key = ?", "restore-stop-marker").First(&marker).Error; err != nil || marker.Value != "current" {
				t.Errorf("failed stop replaced business data: value=%q err=%v", marker.Value, err)
			}
			if _, err := os.Stat(invoked); !errors.Is(err, os.ErrNotExist) {
				t.Error("pg_restore applied the dump after failed core termination")
			}
			if _, err := os.Stat(config.GetDBPath() + ".backup"); !errors.Is(err, os.ErrNotExist) {
				t.Error("failed stop moved the original database to a fallback")
			}
		})
	}
}
