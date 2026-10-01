package database

import (
	"os"
	"path/filepath"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func seedMieruMigrationDatabase(t *testing.T, db *gorm.DB) model.ClientRecord {
	t.Helper()
	for _, mdl := range migrationModels() {
		if err := db.AutoMigrate(mdl); err != nil {
			t.Fatal(err)
		}
	}
	row := model.ClientRecord{Email: "migration-owner", UUID: "legacy-uuid", Password: "other-password", MieruUsername: "独立-wire", MieruPassword: "独立-secret", Enable: true, TotalGB: 12345}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{Tag: "migration-mieru", Port: 24301, Protocol: model.Mieru, Enable: true, Settings: `{"transport":"UDP","clients":[]}`}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: row.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{InboundId: inbound.Id, Email: row.Email, Enable: true, Up: 123, Down: 456}).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func requireMieruMigrationRecord(t *testing.T, db *gorm.DB, original model.ClientRecord) {
	t.Helper()
	var got model.ClientRecord
	if err := db.First(&got, original.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.StableID != original.StableID || got.Email != original.Email || got.MieruUsername != original.MieruUsername || got.MieruPassword != original.MieruPassword || got.UUID != original.UUID || got.Password != original.Password || got.TotalGB != original.TotalGB {
		t.Fatal("native credential migration changed canonical identity, independent authentication or legacy data")
	}
	var links int64
	if err := db.Model(&model.ClientInbound{}).Where("client_id = ?", original.Id).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("native migration lost binding: count=%d error=%v", links, err)
	}
	var traffic xray.ClientTraffic
	if err := db.First(&traffic, "email = ?", original.Email).Error; err != nil || traffic.Up != 123 || traffic.Down != 456 {
		t.Fatal("native migration changed existing traffic")
	}
}

func TestMieruSQLiteOldSchemaUpgradePreservesIdentityAndReopen(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	path := filepath.Join(t.TempDir(), "old.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	row := seedMieruMigrationDatabase(t, legacy)
	for _, column := range []string{"mieru_username", "mieru_password"} {
		if err := legacy.Migrator().DropColumn(&model.ClientRecord{}, column); err != nil {
			t.Fatal(err)
		}
	}
	closeGorm(legacy)
	row.MieruUsername, row.MieruPassword = "", ""
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	requireMieruMigrationRecord(t, GetDB(), row)
	if err := GetDB().Model(&model.ClientRecord{}).Where("id = ?", row.Id).Updates(map[string]any{"mieru_username": "restored-wire", "mieru_password": "restored-secret"}).Error; err != nil {
		t.Fatal(err)
	}
	row.MieruUsername, row.MieruPassword = "restored-wire", "restored-secret"
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	requireMieruMigrationRecord(t, GetDB(), row)
}

func TestMieruPostgresOldSchemaUpgradePreservesIdentityAndReopen(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires isolated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("mieru_upgrade")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	legacy, err := gorm.Open(postgres.Open(os.Getenv("XUI_DB_DSN")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	row := seedMieruMigrationDatabase(t, legacy)
	for _, column := range []string{"mieru_username", "mieru_password"} {
		if err := legacy.Migrator().DropColumn(&model.ClientRecord{}, column); err != nil {
			t.Fatal(err)
		}
	}
	closeGorm(legacy)
	row.MieruUsername, row.MieruPassword = "", ""
	if err := InitDB(""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	requireMieruMigrationRecord(t, GetDB(), row)
	if err := GetDB().Model(&model.ClientRecord{}).Where("id = ?", row.Id).Updates(map[string]any{"mieru_username": "restored-wire", "mieru_password": "restored-secret"}).Error; err != nil {
		t.Fatal(err)
	}
	row.MieruUsername, row.MieruPassword = "restored-wire", "restored-secret"
	if err := InitDB(""); err != nil {
		t.Fatal(err)
	}
	requireMieruMigrationRecord(t, GetDB(), row)
}

func TestMieruCrossDatabaseExportPreservesCredentialsAndLedger(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires isolated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("mieru_export")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	source, err := gorm.Open(sqlite.Open(sourcePath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	row := seedMieruMigrationDatabase(t, source)
	closeGorm(source)
	dsn := os.Getenv("XUI_DB_DSN")
	if err := MigrateData(sourcePath, dsn); err != nil {
		t.Fatal(err)
	}
	destination, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGorm(destination)
	requireMieruMigrationRecord(t, destination, row)
	exportPath := filepath.Join(t.TempDir(), "export.db")
	if err := ExportPostgresToSQLite(dsn, exportPath); err != nil {
		t.Fatal(err)
	}
	reopened, err := gorm.Open(sqlite.Open(exportPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGorm(reopened)
	requireMieruMigrationRecord(t, reopened, row)
}
