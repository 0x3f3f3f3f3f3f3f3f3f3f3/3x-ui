package database

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type passwordOwnerRecoveryFixture struct {
	owners   []model.ClientRecord
	inbounds []model.Inbound
	links    []model.ClientInbound
	traffic  xray.ClientTraffic
	total    model.ClientPolicyTotal
}

func seedPasswordOwnerRecovery(t *testing.T, source *gorm.DB) passwordOwnerRecoveryFixture {
	t.Helper()
	f := passwordOwnerRecoveryFixture{owners: []model.ClientRecord{
		{Email: "password-backup-owner", Enable: true, UUID: "canonical-uuid", Password: "canonical-password", Policy: &model.ClientPolicyOptions{UploadBytesPerSecond: 262144, DownloadBytesPerSecond: 1048576, Multiplier: "1.5"}, DesiredPolicyVersion: 7, PolicyFingerprint: "saved-fingerprint"},
		{Email: "password-backup-other", Enable: true, Password: "other-shared-password"},
	}}
	if err := source.Create(&f.owners).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Model(&f.owners[1]).UpdateColumn("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	for index, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		accounts := []map[string]any{
			{"user": "alice", "pass": "resource-password", "ownerClientId": f.owners[0].StableID},
			{"user": "ALICE", "pass": "alias-password", "ownerClientId": f.owners[0].StableID},
			{"user": "用户", "pass": "other-resource-password", "ownerClientId": f.owners[1].StableID},
		}
		settings := map[string]any{"accounts": accounts}
		if protocol == model.Mixed {
			settings["auth"], settings["udp"] = "password", true
		} else {
			settings["requireAuthentication"] = true
		}
		raw, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		inbound := model.Inbound{Protocol: protocol, Port: 24531 + index, Enable: true, Tag: "password-backup-" + string(protocol), Settings: string(raw)}
		if err := source.Create(&inbound).Error; err != nil {
			t.Fatal(err)
		}
		f.inbounds = append(f.inbounds, inbound)
		for _, owner := range f.owners {
			link := model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}
			if err := source.Create(&link).Error; err != nil {
				t.Fatal(err)
			}
			f.links = append(f.links, link)
		}
	}
	f.traffic = xray.ClientTraffic{Email: f.owners[0].Email, Enable: true, Up: 124, Down: 224}
	f.total = model.ClientPolicyTotal{ClientID: f.owners[0].StableID, RawUpload: 124, RawDownload: 224, BilledBytes: 396}
	for _, row := range []any{&f.traffic, &f.total} {
		if err := source.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Capture persisted defaults for exact comparisons after recovery.
	for i := range f.owners {
		if err := source.First(&f.owners[i], f.owners[i].Id).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := range f.inbounds {
		if err := source.First(&f.inbounds[i], f.inbounds[i].Id).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f passwordOwnerRecoveryFixture) assert(t *testing.T, restored *gorm.DB) {
	t.Helper()
	for _, want := range f.owners {
		var got model.ClientRecord
		if err := restored.First(&got, want.Id).Error; err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("recovery changed canonical identity/credentials/policy: %+v %v", got, err)
		}
	}
	for _, want := range f.inbounds {
		var got model.Inbound
		if err := restored.First(&got, want.Id).Error; err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("recovery changed resource credentials/ownership: %+v %v", got, err)
		}
	}
	for _, want := range f.links {
		var got model.ClientInbound
		if err := restored.First(&got, "client_id = ? AND inbound_id = ?", want.ClientId, want.InboundId).Error; err != nil || got != want {
			t.Fatalf("recovery changed owner membership: %+v %v", got, err)
		}
	}
	var traffic xray.ClientTraffic
	if err := restored.First(&traffic, f.traffic.Id).Error; err != nil || !reflect.DeepEqual(traffic, f.traffic) {
		t.Fatalf("recovery changed history: %+v %v", traffic, err)
	}
	var total model.ClientPolicyTotal
	if err := restored.First(&total, "client_id = ?", f.total.ClientID).Error; err != nil || total != f.total {
		t.Fatalf("recovery changed ledger: %+v %v", total, err)
	}
}

func TestPasswordProxyOwnerBackupRestore(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	dir := t.TempDir()
	if err := InitDB(filepath.Join(dir, "source.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	f := seedPasswordOwnerRecovery(t, GetDB())
	backup, dump, restored := filepath.Join(dir, "backup.db"), filepath.Join(dir, "backup.dump"), filepath.Join(dir, "restored.db")
	if err := BackupSQLite(backup); err != nil {
		t.Fatal(err)
	}
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := DumpSQLite(backup, dump); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(dump, restored); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(restored); err != nil {
		t.Fatal(err)
	}
	f.assert(t, GetDB())
}

func TestPasswordProxyOwnerCrossDatabaseRecovery(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires dedicated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("password_owner_recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	source, err := gorm.Open(sqlite.Open(sourcePath), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGorm(source) })
	for _, row := range migrationModels() {
		if err := source.AutoMigrate(row); err != nil {
			t.Fatal(err)
		}
	}
	f := seedPasswordOwnerRecovery(t, source)
	dsn := os.Getenv("XUI_DB_DSN")
	if err := MigrateData(sourcePath, dsn); err != nil {
		t.Fatal(err)
	}
	destination, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGorm(destination) })
	f.assert(t, destination)
	exported, dump, restored := filepath.Join(dir, "export.db"), filepath.Join(dir, "export.dump"), filepath.Join(dir, "restored.db")
	if err := ExportPostgresToSQLite(dsn, exported); err != nil {
		t.Fatal(err)
	}
	if err := DumpSQLite(exported, dump); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(dump, restored); err != nil {
		t.Fatal(err)
	}
	final, err := gorm.Open(sqlite.Open(restored), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGorm(final) })
	f.assert(t, final)
}
