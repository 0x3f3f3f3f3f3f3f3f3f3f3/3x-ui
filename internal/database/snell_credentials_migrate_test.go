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

func seedSnellMigrationDatabase(t *testing.T, db *gorm.DB) model.ClientRecord {
	t.Helper()
	for _, mdl := range migrationModels() {
		if err := db.AutoMigrate(mdl); err != nil {
			t.Fatal(err)
		}
	}
	row := model.ClientRecord{Email: "migration-owner", UUID: "legacy-uuid", Password: "other-password", SnellPSK: "独立-native-secret", SSHPassword: "ssh-kept", MieruPassword: "mieru-kept", Enable: true, TotalGB: 12345}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{Tag: "migration-snell", Port: 24301, Protocol: model.Snell, Enable: true, Settings: `{"version":5,"clients":[]}`}
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

func requireSnellMigrationRecord(t *testing.T, db *gorm.DB, original model.ClientRecord) {
	t.Helper()
	var got model.ClientRecord
	if err := db.First(&got, original.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.StableID != original.StableID || got.Email != original.Email || got.SnellPSK != original.SnellPSK || got.SSHPassword != original.SSHPassword || got.MieruPassword != original.MieruPassword || got.UUID != original.UUID || got.Password != original.Password || got.TotalGB != original.TotalGB {
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

func TestSnellSQLiteOldSchemaUpgradePreservesIdentityAndReopen(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	path := filepath.Join(t.TempDir(), "old.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	row := seedSnellMigrationDatabase(t, legacy)
	for _, column := range []string{"snell_psk"} {
		if err := legacy.Migrator().DropColumn(&model.ClientRecord{}, column); err != nil {
			t.Fatal(err)
		}
	}
	closeGorm(legacy)
	row.SnellPSK = ""
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	requireSnellMigrationRecord(t, GetDB(), row)
	if err := GetDB().Model(&model.ClientRecord{}).Where("id = ?", row.Id).Updates(map[string]any{"snell_psk": "restored-native-secret"}).Error; err != nil {
		t.Fatal(err)
	}
	row.SnellPSK = "restored-native-secret"
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	requireSnellMigrationRecord(t, GetDB(), row)
}

func TestSnellPostgresOldSchemaUpgradePreservesIdentityAndReopen(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires isolated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("snell_upgrade")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	legacy, err := gorm.Open(postgres.Open(os.Getenv("XUI_DB_DSN")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	row := seedSnellMigrationDatabase(t, legacy)
	for _, column := range []string{"snell_psk"} {
		if err := legacy.Migrator().DropColumn(&model.ClientRecord{}, column); err != nil {
			t.Fatal(err)
		}
	}
	closeGorm(legacy)
	row.SnellPSK = ""
	if err := InitDB(""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	requireSnellMigrationRecord(t, GetDB(), row)
	if err := GetDB().Model(&model.ClientRecord{}).Where("id = ?", row.Id).Updates(map[string]any{"snell_psk": "restored-native-secret"}).Error; err != nil {
		t.Fatal(err)
	}
	row.SnellPSK = "restored-native-secret"
	if err := InitDB(""); err != nil {
		t.Fatal(err)
	}
	requireSnellMigrationRecord(t, GetDB(), row)
}

func TestSnellCrossDatabaseExportPreservesCredentialsAndLedger(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires isolated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("snell_export")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	source, err := gorm.Open(sqlite.Open(sourcePath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	row := seedSnellMigrationDatabase(t, source)
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
	requireSnellMigrationRecord(t, destination, row)
	exportPath := filepath.Join(t.TempDir(), "export.db")
	if err := ExportPostgresToSQLite(dsn, exportPath); err != nil {
		t.Fatal(err)
	}
	reopened, err := gorm.Open(sqlite.Open(exportPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGorm(reopened)
	requireSnellMigrationRecord(t, reopened, row)
}
