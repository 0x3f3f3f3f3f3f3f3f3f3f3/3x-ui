package database

import (
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
	imported := model.ClientRecord{Email: "cross-dialect", PolicyID: rows[0].PolicyID}
	if err := source.Create(&imported).Error; err != nil {
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
	if copied.PolicyID != imported.PolicyID {
		t.Fatalf("SQLite→Postgres replaced identity %q with %q", imported.PolicyID, copied.PolicyID)
	}
}
