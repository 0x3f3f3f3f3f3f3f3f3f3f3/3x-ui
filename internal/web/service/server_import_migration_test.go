package service

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestImportSQLiteMigrationFailureKeepsFallbackAndCoreStopped(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	setupConflictDB(t)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if process := currentXrayProcess(); process != nil && process.IsRunning() {
		t.Fatal("test requires an isolated stopped core")
	}
	previouslyStopped := isManuallyStopped.Load()
	t.Cleanup(func() { isManuallyStopped.Store(previouslyStopped) })
	db := database.GetDB()
	if err := db.Create(&model.Setting{Key: "ownedMigrationSentinel", Value: "previous database"}).Error; err != nil {
		t.Fatal(err)
	}
	uploadPath := filepath.Join(t.TempDir(), "upload.db")
	if err := database.BackupSQLite(uploadPath); err != nil {
		t.Fatal(err)
	}
	upload, err := sql.Open("sqlite3", uploadPath)
	if err != nil {
		t.Fatal(err)
	}
	defer upload.Close()
	if _, err := upload.Exec(`INSERT INTO inbounds(protocol,settings,stream_settings,enable,listen,port,tag) VALUES('vless','{broken json','{}',0,'127.0.0.1',0,'bad-import')`); err != nil {
		t.Fatal(err)
	}
	if err := upload.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(uploadPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = (&ServerService{}).ImportDB(file, true)
	if err == nil || !strings.Contains(err.Error(), "imported DB migration failed") || !strings.Contains(err.Error(), "MigrationRequirements failed") {
		t.Fatalf("import swallowed migration failure: %v", err)
	}
	// RestartXray clears this flag before configuration generation: this also
	// detects a deferred restart that failed before launching an executable.
	if !isManuallyStopped.Load() {
		t.Fatal("failed import attempted to restart the core")
	}
	previous, err := sql.Open("sqlite3", config.GetDBPath()+".backup?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	var sentinel string
	if err := previous.QueryRow(`SELECT value FROM settings WHERE key='ownedMigrationSentinel'`).Scan(&sentinel); err != nil || sentinel != "previous database" {
		t.Fatalf("previous database backup lost: %q %v", sentinel, err)
	}
	var invalid int
	if err := previous.QueryRow(`SELECT COUNT(*) FROM inbounds WHERE tag='bad-import'`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatalf("fallback contains imported data: %d %v", invalid, err)
	}
}

func TestImportPostgresMigrationFailureLeavesCoreStopped(t *testing.T) {
	base := os.Getenv("XUI_E2E_PG_DSN")
	if base == "" {
		t.Skip("set XUI_E2E_PG_DSN to an isolated PostgreSQL URL")
	}
	for _, kind := range []string{"sqlite", "pg-dump"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "pg-dump" {
				for _, tool := range []string{"pg_dump", "pg_restore"} {
					if _, err := exec.LookPath(tool); err != nil {
						t.Skipf("%s required", tool)
					}
				}
			}
			uploadPath := filepath.Join(t.TempDir(), "upload")
			bad := &model.Inbound{
				Tag: "bad-import", Port: 30211, Protocol: model.VLESS,
				Settings: `{broken json`, StreamSettings: `{}`,
			}
			if kind == "sqlite" {
				t.Run("source", func(t *testing.T) {
					t.Setenv("XUI_DB_TYPE", "sqlite")
					t.Setenv("XUI_DB_DSN", "")
					setupConflictDB(t)
					if err := database.GetDB().Create(bad).Error; err != nil {
						t.Fatal(err)
					}
					if err := database.BackupSQLite(uploadPath); err != nil {
						t.Fatal(err)
					}
				})
			}
			t.Setenv("XUI_TEST_PG_DSN", base)
			managedUsagePostgresSchema(t)
			setupConflictDB(t)
			t.Setenv("XUI_BIN_FOLDER", t.TempDir())
			if process := currentXrayProcess(); process != nil && process.IsRunning() {
				t.Fatal("test requires an isolated stopped core")
			}
			previouslyStopped := isManuallyStopped.Load()
			t.Cleanup(func() { isManuallyStopped.Store(previouslyStopped) })
			if kind == "pg-dump" {
				db := database.GetDB()
				if err := db.Create(bad).Error; err != nil {
					t.Fatal(err)
				}
				var schema string
				if err := db.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(schema, "xui_managed_usage_") {
					t.Fatalf("refusing unowned schema: %s", schema)
				}
				env, name, err := pgConnEnv(config.GetDBDSN())
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-owner", "--no-privileges", "--schema", schema, "--dbname", name, "--file", uploadPath)
				cmd.Env = env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("dump owned schema: %v %s", err, out)
				}
				if err := db.Delete(bad).Error; err != nil {
					t.Fatal(err)
				}
			}
			file, err := os.Open(uploadPath)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			err = (&ServerService{}).ImportDB(file, false)
			if err == nil || !strings.Contains(err.Error(), "DB migration failed") || !strings.Contains(err.Error(), "MigrationRequirements failed") {
				t.Fatalf("import swallowed migration failure: %v", err)
			}
			if !isManuallyStopped.Load() {
				t.Fatal("failed import attempted to restart the core")
			}
			var stored model.Inbound
			if err := database.GetDB().Where("tag = ?", bad.Tag).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Settings != bad.Settings {
				t.Fatal("failed migration changed imported settings")
			}
		})
	}
}
