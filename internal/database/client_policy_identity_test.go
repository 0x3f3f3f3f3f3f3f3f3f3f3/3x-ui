package database

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyIdentityMigrationPreservesUsageAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE clients (id INTEGER PRIMARY KEY, email TEXT, enable BOOLEAN)",
		"CREATE TABLE client_traffics (id INTEGER PRIMARY KEY, email TEXT, up BIGINT, down BIGINT, total BIGINT)",
		"INSERT INTO clients VALUES (1, 'old-client', 1), (2, 'other-client', 0)",
		"INSERT INTO client_traffics VALUES (1, 'old-client', 111, 222, 4096)",
	} {
		if err := legacy.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	sqlDB, err := legacy.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	var first, other model.ClientRecord
	if err := GetDB().First(&first, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := GetDB().First(&other, 2).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(first.PolicyID); err != nil {
		t.Fatalf("legacy policy identity is not a UUID: %q", first.PolicyID)
	}
	if _, err := uuid.Parse(other.PolicyID); err != nil || other.PolicyID == first.PolicyID {
		t.Fatalf("independent clients have invalid/shared identities: %q, %q", first.PolicyID, other.PolicyID)
	}
	var traffic xray.ClientTraffic
	if err := GetDB().Where("email = ?", first.Email).First(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	if traffic.Up != 111 || traffic.Down != 222 || traffic.Total != 4096 || other.Enable {
		t.Fatalf("migration changed usage/quota/disable: %+v, other enabled=%v", traffic, other.Enable)
	}
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	var reopened model.ClientRecord
	if err := GetDB().First(&reopened, first.Id).Error; err != nil {
		t.Fatal(err)
	}
	if reopened.PolicyID != first.PolicyID {
		t.Fatalf("reopen replaced policy identity: %q != %q", reopened.PolicyID, first.PolicyID)
	}
}

func TestClientPolicyIdentityIsNotEditableOrRecycledWithEmail(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	var client model.ClientRecord
	if err := json.Unmarshal([]byte(`{"email":"same-label","policyId":"aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"}`), &client); err != nil {
		t.Fatal(err)
	}
	if err := GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	original := client.PolicyID
	if _, err := uuid.Parse(original); err != nil || original == "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa" {
		t.Fatalf("caller selected policy identity, or UUID missing: %q", original)
	}
	client.Email = "renamed"
	client.PolicyID = uuid.NewString()
	if err := GetDB().Save(&client).Error; err != nil {
		t.Fatal(err)
	}
	var persisted model.ClientRecord
	if err := GetDB().First(&persisted, client.Id).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Email != "renamed" || persisted.PolicyID != original {
		t.Fatalf("editing the label replaced identity: %+v", persisted)
	}
	if err := GetDB().Model(&persisted).Update("policy_id", uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	if err := GetDB().First(&persisted, client.Id).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.PolicyID != original {
		t.Fatalf("column update changed identity: %q", persisted.PolicyID)
	}
	if err := GetDB().Delete(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	recreated := model.ClientRecord{Email: "renamed"}
	if err := GetDB().Create(&recreated).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(recreated.PolicyID); err != nil || recreated.PolicyID == original {
		t.Fatalf("recreated email inherited old policy identity: %q", recreated.PolicyID)
	}
}

func TestClientPolicyIdentityBackfillsMoreThanOneBatchAndRestores(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec("CREATE TABLE clients (id INTEGER PRIMARY KEY, email TEXT, policy_id TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 1003; i++ {
		if err := legacy.Exec("INSERT INTO clients (id,email) VALUES (?,?)", i, fmt.Sprintf("legacy-%d", i)).Error; err != nil {
			t.Fatal(err)
		}
	}
	existing := "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	if err := legacy.Exec("UPDATE clients SET policy_id = ? WHERE id = 501", existing).Error; err != nil {
		t.Fatal(err)
	}
	handle, err := legacy.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	var rows []model.ClientRecord
	if err := GetDB().Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1003 {
		t.Fatalf("migrated %d clients; want 1003", len(rows))
	}
	identities := make(map[int]string, len(rows))
	unique := make(map[string]bool, len(rows))
	for _, row := range rows {
		if _, err := uuid.Parse(row.PolicyID); err != nil || unique[row.PolicyID] {
			t.Fatalf("client %d has missing/duplicate policy identity %q", row.Id, row.PolicyID)
		}
		identities[row.Id] = row.PolicyID
		unique[row.PolicyID] = true
	}
	if identities[501] != existing {
		t.Fatalf("migration rewrote a preexisting policy identity: %q", identities[501])
	}
	dump := filepath.Join(t.TempDir(), "backup.sql")
	if err := DumpSQLite(path, dump); err != nil {
		t.Fatal(err)
	}
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	if err := RestoreSQLite(dump, restored); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(restored); err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := GetDB().Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1003 {
		t.Fatalf("restore retained %d identities; want 1003", len(rows))
	}
	for _, row := range rows {
		if row.PolicyID != identities[row.Id] {
			t.Fatalf("restoring client %d replaced identity %q with %q", row.Id, identities[row.Id], row.PolicyID)
		}
	}
}
