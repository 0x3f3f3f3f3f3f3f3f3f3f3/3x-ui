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
	clients := []model.ClientRecord{{Email: "one", Policy: &model.ClientPolicyOptions{UploadBytesPerSecond: 262144, DownloadBytesPerSecond: 1048576, Multiplier: "1.5"}, DesiredPolicyVersion: 7, PolicyFingerprint: "stored-policy-fingerprint"}, {Email: "two"}}
	if err := src.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	source := model.ClientPolicySource{InstanceID: "migrated-source", NodeKey: "local", Epoch: 3, Sequence: 17}
	total := model.ClientPolicyTotal{ClientID: clients[0].StableID, RawUpload: 11, RawDownload: 22, BilledBytes: 66, UncertainBytes: 5}
	receipt := model.ClientPolicyReceipt{FirstUsedAt: 123450, InstanceID: source.InstanceID, ClientID: total.ClientID, Epoch: 2, Sequence: 17, PolicyVersion: 4, RawUpload: 11, RawDownload: 22, BilledBytes: 66, UncertainBytes: 5, Remainder: 1234}
	reset := model.ClientPolicyReset{ClientID: clients[0].StableID, RequestID: "migrated-reset", InstanceID: source.InstanceID, Epoch: 2, Sequence: 10, RawUpload: 4, RawDownload: 5, BilledBytes: 9, Remainder: 500000, UncertainBytes: 2, PolicyVersion: 3, CreatedAt: 123456}
	batch := model.ClientTrafficResetBatch{RequestID: "migrated-batch", Scope: "calendar:daily:original", ScheduledAt: 123000, InboundIDsJSON: `[3,8]`, SelectionHash: "selected-members", TargetsJSON: `[{"clientId":"original-identity","email":"original-email","enableLegacy":true}]`, ManagedIDsJSON: `["original-identity"]`, Applied: true, Affected: 1, CreatedAt: 123456}
	resetTime := model.ClientTrafficResetTime{ClientID: clients[0].StableID, EffectiveAt: 123456}
	for _, row := range []any{&source, &total, &receipt, &reset, &batch, &resetTime} {
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
	var gotReset model.ClientPolicyReset
	if err := dst.First(&gotReset).Error; err != nil || gotReset != reset {
		t.Fatalf("reset migration lost request or exact boundary: %+v %v", gotReset, err)
	}
	var gotBatch model.ClientTrafficResetBatch
	if err := dst.First(&gotBatch).Error; err != nil || gotBatch != batch {
		t.Fatalf("reset batch migration lost immutable membership or completion: %+v %v", gotBatch, err)
	}
	var gotTime model.ClientTrafficResetTime
	if err := dst.First(&gotTime).Error; err != nil || gotTime != resetTime {
		t.Fatalf("reset migration lost calendar ordering: %+v, %v", gotTime, err)
	}
	var gotTotal model.ClientPolicyTotal
	if err := dst.First(&gotTotal).Error; err != nil || gotTotal != total {
		t.Fatalf("total migration lost state: %+v %v", gotTotal, err)
	}
	var gotClient model.ClientRecord
	if err := dst.First(&gotClient, clients[0].Id).Error; err != nil || gotClient.StableID != clients[0].StableID {
		t.Fatal("identity migration lost stable mapping")
	}
	if gotClient.Policy == nil || *gotClient.Policy != *clients[0].Policy || gotClient.DesiredPolicyVersion != 7 || gotClient.PolicyFingerprint != "stored-policy-fingerprint" {
		t.Fatalf("policy migration lost settings/version: %+v", gotClient)
	}
	if err := src.Migrator().DropColumn(&model.ClientPolicyReceipt{}, "first_used_at"); err != nil {
		t.Fatal(err)
	}
	if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err != nil {
		t.Fatalf("pre-first-use ledger migration: %v", err)
	}
	oldReceipt := model.ClientPolicyReceipt{}
	wantOldReceipt := receipt
	wantOldReceipt.FirstUsedAt = 0
	if err := dst.First(&oldReceipt).Error; err != nil || oldReceipt != wantOldReceipt {
		t.Fatalf("old ledger lost history or fabricated first use: %+v, %v", oldReceipt, err)
	}
	if err := src.Migrator().DropIndex(&model.ClientTrafficResetBatch{}, "idx_reset_calendar"); err != nil {
		t.Fatal(err)
	}
	if err := src.Migrator().DropIndex(&model.ClientTrafficResetBatch{}, "idx_reset_pending"); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"scheduled_at", "inbound_ids_json", "last_attempt_at"} {
		if err := src.Migrator().DropColumn(&model.ClientTrafficResetBatch{}, column); err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err != nil {
		t.Fatalf("pre-calendar batch columns migration: %v", err)
	}
	legacyBatch := model.ClientTrafficResetBatch{}
	wantLegacy := batch
	wantLegacy.ScheduledAt, wantLegacy.InboundIDsJSON = 0, "[]"
	if err := dst.First(&legacyBatch).Error; err != nil || legacyBatch != wantLegacy {
		t.Fatalf("legacy batch migration invented calendar metadata or lost membership: %+v, %v", legacyBatch, err)
	}
	if err := src.Migrator().DropTable(&model.ClientTrafficResetTime{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err != nil {
		t.Fatalf("pre-calendar ledger schema migration: %v", err)
	}
	var timeCount int64
	if err := dst.Model(&model.ClientTrafficResetTime{}).Count(&timeCount).Error; err != nil || timeCount != 0 {
		t.Fatalf("old schema fabricated calendar ordering: %d, %v", timeCount, err)
	}
	if err := src.Migrator().DropTable(&model.ClientTrafficResetBatch{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err != nil {
		t.Fatalf("pre-batch ledger schema migration: %v", err)
	}
	var batchCount int64
	if err := dst.Model(&model.ClientTrafficResetBatch{}).Count(&batchCount).Error; err != nil || batchCount != 0 {
		t.Fatalf("old schema fabricated batch membership: %d %v", batchCount, err)
	}
	if err := src.Migrator().DropTable(&model.ClientPolicyReset{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err != nil {
		t.Fatalf("pre-reset ledger schema migration: %v", err)
	}
	var resetCount int64
	if err := dst.Model(&model.ClientPolicyReset{}).Count(&resetCount).Error; err != nil || resetCount != 0 {
		t.Fatalf("old schema fabricated reset history: %d %v", resetCount, err)
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
	for _, column := range []string{"policy_upload_bytes_per_second", "policy_download_bytes_per_second", "policy_multiplier", "desired_policy_version", "policy_fingerprint"} {
		if err := src.Migrator().DropColumn(&model.ClientRecord{}, column); err != nil {
			t.Fatal(err)
		}
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
	if migrated[0].Policy != nil || migrated[0].DesiredPolicyVersion != 0 {
		t.Fatal("legacy migration activated policy options or a version")
	}
	if src.Migrator().HasColumn(&model.ClientRecord{}, "StableID") {
		t.Fatal("migration modified source schema")
	}
	if err := src.AutoMigrate(&model.ClientPolicyReset{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateData(path, os.Getenv("XUI_DB_DSN")); err == nil || err.Error() != "source has client policy resets without a complete ledger" {
		t.Fatalf("accepted resets without the lifetime ledger: %v", err)
	}
}
