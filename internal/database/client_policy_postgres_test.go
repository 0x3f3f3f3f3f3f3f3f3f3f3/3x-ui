package database

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyIdentityMigration_Postgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("XUI_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("set XUI_TEST_PG_DSN to an isolated PostgreSQL instance")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	schema := fmt.Sprintf("client_policy_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = CloseDB()
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	scoped := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := u.Query()
		query.Set("search_path", schema)
		u.RawQuery = query.Encode()
		scoped = u.String()
	}
	t.Setenv("XUI_DB_TYPE", "postgres")
	t.Setenv("XUI_DB_DSN", scoped)
	legacy, err := gorm.Open(postgres.Open(scoped), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	legacyHandle, err := legacy.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacyHandle.Close() })
	for _, statement := range []string{
		"CREATE TABLE clients (id BIGSERIAL PRIMARY KEY, email TEXT, enable BOOLEAN)",
		"INSERT INTO clients (email,enable) SELECT 'legacy-' || n, false FROM generate_series(1, 1003) n",
	} {
		if err := legacy.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := InitDB(""); err != nil {
		t.Fatal(err)
	}
	var rows []model.ClientRecord
	if err := GetDB().Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1003 {
		t.Fatalf("migrated %d PostgreSQL clients; want 1003", len(rows))
	}
	var missingSSH int64
	if err := GetDB().Model(&model.ClientRecord{}).Where("ssh_config IS NULL").Count(&missingSSH).Error; err != nil || missingSSH != 0 {
		t.Fatalf("PostgreSQL migration left NULL SSH configuration: %v", err)
	}
	unique := make(map[string]bool, len(rows))
	for _, row := range rows {
		if _, err := uuid.Parse(row.PolicyID); err != nil || unique[row.PolicyID] || row.Enable {
			t.Fatalf("migration lost identity/disable for client %d: %q, enabled %v", row.Id, row.PolicyID, row.Enable)
		}
		unique[row.PolicyID] = true
	}
	if err := InitDB(""); err != nil {
		t.Fatal(err)
	}
	var reopened model.ClientRecord
	if err := GetDB().First(&reopened, rows[0].Id).Error; err != nil {
		t.Fatal(err)
	}
	if reopened.PolicyID != rows[0].PolicyID {
		t.Fatal("PostgreSQL reopen replaced the billing identity")
	}
	duplicate := model.ClientRecord{Email: "collision", PolicyID: rows[0].PolicyID}
	if err := GetDB().Create(&duplicate).Error; err == nil || !strings.Contains(err.Error(), "23505") {
		t.Fatalf("duplicate policy identity was not rejected by the unique index: %v", err)
	}
	sqliteCopy := filepath.Join(t.TempDir(), "migration-source.db")
	source, err := gorm.Open(sqlite.Open(sqliteCopy), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sourceHandle, err := source.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sourceHandle.Close() })
	for _, mdl := range migrationModels() {
		if err := source.AutoMigrate(mdl); err != nil {
			t.Fatal(err)
		}
	}
	imported := model.ClientRecord{Email: "cross-dialect", PolicyID: rows[0].PolicyID, SSHConfig: `{"publicKeys":["public-only-fixture"],"targets":[{"host":"example.invalid","port":443}],"reverse":[{"address":"::1","port":32001}]}`}
	if err := source.Create(&imported).Error; err != nil {
		t.Fatal(err)
	}
	policy := model.ClientPolicySettings{PolicyID: imported.PolicyID, UploadBps: 65536, DownloadBps: 131072, Scope: "local", Version: 3}
	if err := source.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Create(&xray.ClientTraffic{Email: imported.Email, Up: 7, Down: 11}).Error; err != nil {
		t.Fatal(err)
	}
	ledger := NewClientUsageLedger(source)
	if _, err := ledger.ChangeMultiplier(context.Background(), imported.PolicyID, 1, 1500, nil); err != nil {
		t.Fatal(err)
	}
	meter := usageMeter(t, ledger, imported, "local/xray")
	report := ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 3}
	if _, err := ledger.Apply(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if err := sourceHandle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateData(sqliteCopy, scoped); err != nil {
		t.Fatal(err)
	}
	var copied model.ClientRecord
	if err := GetDB().Where("email = ?", imported.Email).First(&copied).Error; err != nil {
		t.Fatal(err)
	}
	if copied.SSHConfig != imported.SSHConfig {
		t.Fatal("SQLite to PostgreSQL migration lost SSH credentials or permissions")
	}
	var copiedPolicy model.ClientPolicySettings
	if err := GetDB().First(&copiedPolicy, "policy_id = ?", imported.PolicyID).Error; err != nil || copiedPolicy != policy {
		t.Fatalf("SQLite to PostgreSQL migration lost policy rates or version: %+v, %v", copiedPolicy, err)
	}
	if copied.PolicyID != imported.PolicyID {
		t.Fatalf("SQLite→Postgres replaced identity %q with %q", imported.PolicyID, copied.PolicyID)
	}
	ledger = NewClientUsageLedger(GetDB())
	if d, err := ledger.Apply(context.Background(), report); err != nil || d != (ClientUsageDelta{}) {
		t.Fatalf("cross-dialect restore double-billed an acknowledged report: %+v, %v", d, err)
	}
	if a := usageRead(t, ledger, imported); a.Up != 10 || a.Down != 11 || a.Billed != 22 || a.Remainder != 500 || a.Revision != 2 || a.Multiplier != 1500 {
		t.Fatalf("cross-dialect restore lost ledger/cursor consistency: %+v", a)
	}
}
