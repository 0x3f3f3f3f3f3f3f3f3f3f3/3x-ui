package database

import (
	"os"
	"path/filepath"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
)

func TestLegacyTrafficReceiptCrossDatabaseMigration(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires dedicated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("legacy_traffic_migration")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	for _, withReceipt := range []bool{true, false} {
		name := "old-schema"
		if withReceipt {
			name = "with-receipt"
		}
		t.Run(name, func(t *testing.T) {
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
			want := model.LegacyTrafficReceipt{ProcessID: "original-child", Sequence: 17, BatchID: "original-batch"}
			if withReceipt {
				if err := src.Create(&want).Error; err != nil {
					t.Fatal(err)
				}
			} else if err := src.Migrator().DropTable(&model.LegacyTrafficReceipt{}); err != nil {
				t.Fatal(err)
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
				var receipts []model.LegacyTrafficReceipt
				if err := db.Find(&receipts).Error; err != nil {
					t.Fatal(err)
				}
				if withReceipt && (len(receipts) != 1 || receipts[0] != want) {
					t.Fatalf("migration lost settlement identity: %+v", receipts)
				}
				if !withReceipt && len(receipts) != 0 {
					t.Fatalf("old schema fabricated receipts: %+v", receipts)
				}
				var count int64
				if err := db.Model(&model.User{}).Where("username = ?", "retained-user").Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("migration lost existing user: %d, %v", count, err)
				}
			}
			if src.Migrator().HasTable(&model.LegacyTrafficReceipt{}) != withReceipt {
				t.Fatal("migration modified source schema")
			}
		})
	}
}

func openLegacyTrafficMigrationDB(t *testing.T, dialect gorm.Dialector) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGorm(db) })
	return db
}
