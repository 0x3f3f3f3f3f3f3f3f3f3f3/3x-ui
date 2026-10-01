package database

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
)

func seedLegacyConfigMigration(t *testing.T, db *gorm.DB, known bool) {
	t.Helper()
	for _, m := range allModels() {
		if err := db.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.User{Username: "config-proof-user", Password: "unused"}).Error; err != nil {
		t.Fatal(err)
	}
	receipt := model.LegacyTrafficReceipt{
		ProcessID: "proof-old-source", Sequence: 1, BatchID: "proof-old-batch",
		PayloadDigest: "8b9f6a84f47682093860a2a41cc83708435f6e74454285de7b2d497bf6822efa",
	}
	if err := db.Create(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("old-wire"))
	bucket := model.LegacyUnassignedTraffic{ProcessID: receipt.ProcessID, LabelHash: hex.EncodeToString(hash[:]), Label: "old-wire", SourceMode: "legacy", RawUpload: 7, RawDownload: 11}
	if err := db.Create(&bucket).Error; err != nil {
		t.Fatal(err)
	}
	if !known {
		if err := db.Migrator().DropTable(&model.LegacyTrafficConfigSource{}); err != nil {
			t.Fatal(err)
		}
		return
	}
	for i, stable := range []bool{true, false} {
		source := model.LegacyTrafficConfigSource{
			ProcessID:    "proof-source-" + strings.Repeat("x", i+1),
			ConfigDigest: strings.Repeat("ab", 32), EffectiveConfigDigest: strings.Repeat("cd", 32), ConfigStable: stable,
		}
		if err := db.Create(&source).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.LegacyTrafficConfigSource{ProcessID: "proof-unknown"}).Error; err != nil {
		t.Fatal(err)
	}
}

func assertLegacyConfigMigration(t *testing.T, db *gorm.DB, known bool) {
	t.Helper()
	var sources []model.LegacyTrafficConfigSource
	if err := db.Order("process_id").Find(&sources).Error; err != nil {
		t.Fatal(err)
	}
	if !known && len(sources) != 0 || known && len(sources) != 3 {
		t.Fatalf("migration lost or invented startup evidence: %+v", sources)
	}
	if known {
		for _, source := range sources {
			if source.ProcessID == "proof-unknown" {
				if source.ConfigStable || source.ConfigDigest != "" || source.EffectiveConfigDigest != "" {
					t.Fatalf("unknown startup acquired a fabricated proof: %+v", source)
				}
			} else if source.ConfigDigest != strings.Repeat("ab", 32) || source.EffectiveConfigDigest != strings.Repeat("cd", 32) || source.ConfigStable != (source.ProcessID == "proof-source-x") {
				t.Fatalf("migration changed startup identity or stability: %+v", source)
			}
		}
	}
	var receipt model.LegacyTrafficReceipt
	if err := db.First(&receipt).Error; err != nil || receipt.ProcessID != "proof-old-source" || receipt.Sequence != 1 || receipt.BatchID != "proof-old-batch" || receipt.PayloadDigest != "8b9f6a84f47682093860a2a41cc83708435f6e74454285de7b2d497bf6822efa" {
		t.Fatalf("migration changed authentic original receipt: %+v/%v", receipt, err)
	}
	var bucket model.LegacyUnassignedTraffic
	if err := db.First(&bucket).Error; err != nil || bucket.ProcessID != receipt.ProcessID || bucket.Label != "old-wire" || bucket.SourceMode != "legacy" || bucket.RawUpload != 7 || bucket.RawDownload != 11 {
		t.Fatalf("migration changed authentic raw counters: %+v/%v", bucket, err)
	}
}

func TestLegacyTrafficConfigSourceSQLiteBackupAndOldUpgrade(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	for _, known := range []bool{true, false} {
		name := "current"
		if !known {
			name = "missing-source"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.db")
			if err := InitDB(path); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = CloseDB() })
			seedLegacyConfigMigration(t, GetDB(), known)
			if err := CloseDB(); err != nil {
				t.Fatal(err)
			}
			if err := InitDB(path); err != nil {
				t.Fatal(err)
			}
			assertLegacyConfigMigration(t, GetDB(), known)
			backup := filepath.Join(t.TempDir(), "backup.db")
			if err := BackupSQLite(backup); err != nil {
				t.Fatal(err)
			}
			assertLegacyConfigMigration(t, openLegacyTrafficMigrationDB(t, sqlite.Open(backup)), known)
		})
	}
}

func TestLegacyTrafficConfigSourceCrossDatabaseMigration(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires dedicated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("legacy_config_source_migration")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	for _, known := range []bool{true, false} {
		name := "current"
		if !known {
			name = "missing-source"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.db")
			src := openLegacyTrafficMigrationDB(t, sqlite.Open(path))
			seedLegacyConfigMigration(t, src, known)
			if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err != nil {
				t.Fatal(err)
			}
			assertLegacyConfigMigration(t, openLegacyTrafficMigrationDB(t, postgres.Open(os.Getenv("XUI_DB_DSN"))), known)
			export := filepath.Join(t.TempDir(), "export.db")
			if err := ExportPostgresToSQLite(os.Getenv("XUI_DB_DSN"), export); err != nil {
				t.Fatal(err)
			}
			assertLegacyConfigMigration(t, openLegacyTrafficMigrationDB(t, sqlite.Open(export)), known)
			if src.Migrator().HasTable(&model.LegacyTrafficConfigSource{}) != known {
				t.Fatal("migration changed historical source schema")
			}
		})
	}
}

func TestLegacyTrafficConfigSourceOldPostgresExport(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires dedicated PostgreSQL source")
	}
	cleanup, err := testpg.IsolatePackage("legacy_config_source_old_export")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	src := openLegacyTrafficMigrationDB(t, postgres.Open(os.Getenv("XUI_DB_DSN")))
	seedLegacyConfigMigration(t, src, false)
	export := filepath.Join(t.TempDir(), "old-export.db")
	if err := ExportPostgresToSQLite(os.Getenv("XUI_DB_DSN"), export); err != nil {
		t.Fatal(err)
	}
	assertLegacyConfigMigration(t, openLegacyTrafficMigrationDB(t, sqlite.Open(export)), false)
	if src.Migrator().HasTable(&model.LegacyTrafficConfigSource{}) {
		t.Fatal("native export changed old source schema")
	}
}
