package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestClientPolicyCrossDatabaseMigration(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires dedicated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("policy_migration")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	path := filepath.Join(t.TempDir(), "source.db")
	src, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := src.DB()
	t.Cleanup(func() { _ = handle.Close() })
	for _, m := range migrationModels() {
		if err := src.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	clients := []model.ClientRecord{{Email: "one"}, {Email: "two"}}
	if err := src.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	source := model.ClientPolicySource{InstanceID: "migrated-source", NodeKey: "local", Epoch: 3, Sequence: 17}
	total := model.ClientPolicyTotal{ClientID: clients[0].StableID, RawUpload: 11, RawDownload: 22, BilledBytes: 66, UncertainBytes: 5}
	receipt := model.ClientPolicyReceipt{InstanceID: source.InstanceID, ClientID: total.ClientID, Epoch: 2, Sequence: 17, PolicyVersion: 4, RawUpload: 11, RawDownload: 22, BilledBytes: 66, UncertainBytes: 5, Remainder: 1234}
	for _, row := range []any{&source, &total, &receipt} {
		if err := src.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err != nil {
		t.Fatal(err)
	}
	dst, err := gorm.Open(postgres.Open(os.Getenv("XUI_DB_DSN")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dstHandle, _ := dst.DB()
	t.Cleanup(func() { _ = dstHandle.Close() })
	var gotReceipt model.ClientPolicyReceipt
	if err := dst.First(&gotReceipt).Error; err != nil || gotReceipt != receipt {
		t.Fatalf("receipt migration lost state: %+v %v", gotReceipt, err)
	}
	var gotTotal model.ClientPolicyTotal
	if err := dst.First(&gotTotal).Error; err != nil || gotTotal != total {
		t.Fatalf("total migration lost state: %+v %v", gotTotal, err)
	}
	var gotClient model.ClientRecord
	if err := dst.First(&gotClient, clients[0].Id).Error; err != nil || gotClient.StableID != clients[0].StableID {
		t.Fatal("identity migration lost stable mapping")
	}
	if err := src.Migrator().DropTable(&model.ClientPolicyReceipt{}, &model.ClientPolicyTotal{}, &model.ClientPolicySource{}); err != nil {
		t.Fatal(err)
	}
	if err := src.Migrator().DropIndex(&model.ClientRecord{}, "idx_clients_stable_id"); err != nil {
		t.Fatal(err)
	}
	if err := src.Migrator().DropColumn(&model.ClientRecord{}, "StableID"); err != nil {
		t.Fatal(err)
	}
	if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err != nil {
		t.Fatalf("legacy cross-database migration: %v", err)
	}
	var migrated []model.ClientRecord
	if err := dst.Order("id").Find(&migrated).Error; err != nil {
		t.Fatal(err)
	}
	if len(migrated) != 2 || migrated[0].StableID == "" || migrated[0].StableID == migrated[1].StableID {
		t.Fatal("legacy clients have missing/shared identities")
	}
	if src.Migrator().HasColumn(&model.ClientRecord{}, "StableID") {
		t.Fatal("migration modified source schema")
	}
}
