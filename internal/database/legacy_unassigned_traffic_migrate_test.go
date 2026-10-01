package database

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
)

func TestLegacyUnassignedTrafficSQLiteBackupAndOldUpgrade(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	for _, old := range []bool{false, true} {
		name := "current-backup"
		if old {
			name = "old-upgrade"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.db")
			if err := InitDB(path); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = CloseDB() })
			want := model.LegacyTrafficReceipt{ProcessID: "backup-source", Sequence: 9, BatchID: "backup-batch", PayloadDigest: strings.Repeat("ab", 32)}
			if err := GetDB().Create(&want).Error; err != nil {
				t.Fatal(err)
			}
			label := strings.Repeat("用户名", 1000)
			hash := sha256.Sum256([]byte(label))
			bucket := model.LegacyUnassignedTraffic{
				ProcessID: want.ProcessID, LabelHash: hex.EncodeToString(hash[:]), Label: label,
				SourceMode: "managed", SourceInstanceID: "original-managed-instance", RawUpload: 123, RawDownload: 456,
			}
			if old {
				if err := GetDB().Migrator().DropTable(&model.LegacyUnassignedTraffic{}); err != nil {
					t.Fatal(err)
				}
				if err := GetDB().Migrator().DropColumn(&model.LegacyTrafficReceipt{}, "PayloadDigest"); err != nil {
					t.Fatal(err)
				}
				want.PayloadDigest = ""
				if err := CloseDB(); err != nil {
					t.Fatal(err)
				}
				if err := InitDB(path); err != nil {
					t.Fatalf("old panel schema cannot be upgraded: %v", err)
				}
			} else if err := GetDB().Create(&bucket).Error; err != nil {
				t.Fatal(err)
			}
			backupPath := filepath.Join(t.TempDir(), "backup.db")
			if err := BackupSQLite(backupPath); err != nil {
				t.Fatal(err)
			}
			backup := openLegacyTrafficMigrationDB(t, sqlite.Open(backupPath))
			var got model.LegacyTrafficReceipt
			if err := backup.First(&got).Error; err != nil || got != want {
				t.Fatalf("backup/upgrade changed original receipt: %+v/%v", got, err)
			}
			var rows []model.LegacyUnassignedTraffic
			if err := backup.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if old && len(rows) != 0 || !old && (len(rows) != 1 || !reflect.DeepEqual(rows[0], bucket)) {
				t.Fatalf("backup/upgrade lost or invented raw evidence: %+v", rows)
			}
		})
	}
}

func TestLegacyUnassignedTrafficCrossDatabaseMigration(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires dedicated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("legacy_unassigned_migration")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	for _, schema := range []string{"current", "no-retention", "old-digest"} {
		t.Run(schema, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.db")
			src := openLegacyTrafficMigrationDB(t, sqlite.Open(path))
			for _, m := range allModels() {
				if err := src.AutoMigrate(m); err != nil {
					t.Fatal(err)
				}
			}
			if err := src.Create(&model.User{Username: "retained-user", Password: "unused"}).Error; err != nil {
				t.Fatal(err)
			}
			wantReceipt := model.LegacyTrafficReceipt{ProcessID: "captured-child", Sequence: 17, BatchID: "captured-batch", PayloadDigest: strings.Repeat("ab", 32)}
			if err := src.Create(&wantReceipt).Error; err != nil {
				t.Fatal(err)
			}
			var buckets []model.LegacyUnassignedTraffic
			for i, label := range []string{"alice", "ALICE", strings.Repeat("用户名", 1000), "legacy\x00username", `legacy\u0000username`} {
				hash := sha256.Sum256([]byte(label))
				buckets = append(buckets, model.LegacyUnassignedTraffic{
					ProcessID: wantReceipt.ProcessID, LabelHash: hex.EncodeToString(hash[:]), Label: label,
					SourceMode: "legacy", RawUpload: int64(11 + i), RawDownload: int64(19 + i),
				})
			}
			if schema == "current" {
				if err := src.Create(&buckets).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				buckets = nil
				if err := src.Migrator().DropTable(&model.LegacyUnassignedTraffic{}); err != nil {
					t.Fatal(err)
				}
			}
			if schema == "old-digest" {
				if err := src.Migrator().DropColumn(&model.LegacyTrafficReceipt{}, "PayloadDigest"); err != nil {
					t.Fatal(err)
				}
				wantReceipt.PayloadDigest = ""
			}
			if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err != nil {
				t.Fatal(err)
			}
			dst := openLegacyTrafficMigrationDB(t, postgres.Open(os.Getenv("XUI_DB_DSN")))
			exportPath := filepath.Join(t.TempDir(), "export.db")
			if err := ExportPostgresToSQLite(os.Getenv("XUI_DB_DSN"), exportPath); err != nil {
				t.Fatal(err)
			}
			exported := openLegacyTrafficMigrationDB(t, sqlite.Open(exportPath))
			for _, db := range []*gorm.DB{dst, exported} {
				var gotReceipt model.LegacyTrafficReceipt
				if err := db.First(&gotReceipt, "process_id = ?", wantReceipt.ProcessID).Error; err != nil || gotReceipt != wantReceipt {
					t.Fatalf("migration lost/invented receipt binding: %+v/%v", gotReceipt, err)
				}
				var gotBuckets []model.LegacyUnassignedTraffic
				if err := db.Find(&gotBuckets).Error; err != nil || len(gotBuckets) != len(buckets) {
					t.Fatalf("migration lost/invented raw source labels: %d/%v", len(gotBuckets), err)
				}
				for _, got := range gotBuckets {
					found := false
					for _, want := range buckets {
						found = found || reflect.DeepEqual(got, want)
					}
					if !found {
						t.Fatalf("migration changed source provenance: %+v", got)
					}
				}
			}
			if src.Migrator().HasTable(&model.LegacyUnassignedTraffic{}) != (schema == "current") || src.Migrator().HasColumn(&model.LegacyTrafficReceipt{}, "PayloadDigest") != (schema != "old-digest") {
				t.Fatal("migration modified historical source schema")
			}
		})
	}
}

func TestLegacyUnassignedTrafficOldPostgresExport(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires dedicated PostgreSQL source")
	}
	cleanup, err := testpg.IsolatePackage("legacy_unassigned_old_export")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	src := openLegacyTrafficMigrationDB(t, postgres.Open(os.Getenv("XUI_DB_DSN")))
	for _, m := range allModels() {
		if err := src.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := src.Create(&model.User{Username: "retained-old-user", Password: "unused"}).Error; err != nil {
		t.Fatal(err)
	}
	want := model.LegacyTrafficReceipt{ProcessID: "old-source", Sequence: 9, BatchID: "old-batch"}
	if err := src.Create(&want).Error; err != nil {
		t.Fatal(err)
	}
	if err := src.Migrator().DropTable(&model.LegacyUnassignedTraffic{}); err != nil {
		t.Fatal(err)
	}
	if err := src.Migrator().DropColumn(&model.LegacyTrafficReceipt{}, "PayloadDigest"); err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(t.TempDir(), "old-export.db")
	if err := ExportPostgresToSQLite(os.Getenv("XUI_DB_DSN"), exportPath); err != nil {
		t.Fatalf("old native export cannot preserve original receipts: %v", err)
	}
	dst := openLegacyTrafficMigrationDB(t, sqlite.Open(exportPath))
	var got model.LegacyTrafficReceipt
	if err := dst.First(&got, "process_id = ?", want.ProcessID).Error; err != nil || got != want {
		t.Fatalf("old export invented or lost receipt data: %+v/%v", got, err)
	}
	var count int64
	if err := dst.Model(&model.LegacyUnassignedTraffic{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("old export invented retained counters: %d/%v", count, err)
	}
	if src.Migrator().HasTable(&model.LegacyUnassignedTraffic{}) || src.Migrator().HasColumn(&model.LegacyTrafficReceipt{}, "PayloadDigest") {
		t.Fatal("export modified the old source schema")
	}
}

func TestLegacyUnassignedTrafficPreReceiptPostgresExport(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires dedicated PostgreSQL source")
	}
	cleanup, err := testpg.IsolatePackage("legacy_unassigned_pre_receipt_export")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	src := openLegacyTrafficMigrationDB(t, postgres.Open(os.Getenv("XUI_DB_DSN")))
	for _, m := range allModels() {
		if err := src.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := src.Create(&model.User{Username: "pre-receipt-user", Password: "unused"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, table := range []any{&model.LegacyUnassignedTraffic{}, &model.LegacyTrafficReceipt{}} {
		if err := src.Migrator().DropTable(table); err != nil {
			t.Fatal(err)
		}
	}
	exportPath := filepath.Join(t.TempDir(), "pre-receipt.db")
	if err := ExportPostgresToSQLite(os.Getenv("XUI_DB_DSN"), exportPath); err != nil {
		t.Fatalf("pre-receipt source cannot be exported: %v", err)
	}
	dst := openLegacyTrafficMigrationDB(t, sqlite.Open(exportPath))
	var users int64
	if err := dst.Model(&model.User{}).Where("username = ?", "pre-receipt-user").Count(&users).Error; err != nil || users != 1 {
		t.Fatalf("pre-receipt export lost historical user: %d/%v", users, err)
	}
	for _, table := range []any{&model.LegacyUnassignedTraffic{}, &model.LegacyTrafficReceipt{}} {
		var count int64
		if err := dst.Model(table).Count(&count).Error; err != nil || count != 0 || src.Migrator().HasTable(table) {
			t.Fatalf("pre-receipt export invented history or changed source: %T %d/%v", table, count, err)
		}
	}
}
