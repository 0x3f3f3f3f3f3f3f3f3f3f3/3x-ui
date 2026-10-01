package database

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
)

type sshRecoveryFixture struct {
	account model.ClientRecord
	host    model.NativeSSHHostKey
	inbound model.Inbound
}

func seedSSHRecovery(t *testing.T, db *gorm.DB) sshRecoveryFixture {
	t.Helper()
	account := seedMieruMigrationDatabase(t, db)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	host := model.NativeSSHHostKey{ID: uuid.NewString(), PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	account.SSHUsername, account.SSHPassword, account.SSHAuthorizedKeys = "business-wire", "business-password", host.PublicKey
	if err := db.Save(&account).Error; err != nil {
		t.Fatal(err)
	}
	var inbound model.Inbound
	if err := db.First(&inbound, "tag = ?", "migration-mieru").Error; err != nil {
		t.Fatal(err)
	}
	inbound.Protocol, inbound.Settings, inbound.SSHHostKeyID = model.SSH, `{"clients":[],"allowPassword":true}`, host.ID
	if err := db.Save(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	return sshRecoveryFixture{account, host, inbound}
}

func (f sshRecoveryFixture) assert(t *testing.T, db *gorm.DB) {
	t.Helper()
	requireMieruMigrationRecord(t, db, f.account)
	var account model.ClientRecord
	if err := db.First(&account, f.account.Id).Error; err != nil {
		t.Fatal(err)
	}
	if account.SSHUsername != f.account.SSHUsername || account.SSHAuthorizedKeys != f.account.SSHAuthorizedKeys || account.SSHPassword != f.account.SSHPassword {
		t.Fatal("recovery changed independent SSH authentication")
	}
	var host model.NativeSSHHostKey
	if err := db.First(&host, "id = ?", f.host.ID).Error; err != nil || host != f.host {
		t.Fatal("recovery changed the database-owned SSH private key or public trust")
	}
	var inbound model.Inbound
	if err := db.First(&inbound, f.inbound.Id).Error; err != nil || inbound.SSHHostKeyID != f.host.ID || inbound.Protocol != model.SSH {
		t.Fatal("recovery detached listener trust from its original key")
	}
}

func TestSSHSQLiteBackupRestorePreservesBusinessTrustAndCanonicalIdentity(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	dir := t.TempDir()
	if err := InitDB(filepath.Join(dir, "source.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	f := seedSSHRecovery(t, GetDB())
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

func TestSSHCrossDatabaseRecoveryPreservesBusinessTrustAndCanonicalIdentity(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires isolated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("ssh_recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	source, err := gorm.Open(sqlite.Open(sourcePath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f := seedSSHRecovery(t, source)
	closeGorm(source)
	dsn := os.Getenv("XUI_DB_DSN")
	if err := MigrateData(sourcePath, dsn); err != nil {
		t.Fatal(err)
	}
	destination, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.assert(t, destination)
	closeGorm(destination)
	exportPath, dump, restored := filepath.Join(dir, "export.db"), filepath.Join(dir, "export.dump"), filepath.Join(dir, "restored.db")
	if err := ExportPostgresToSQLite(dsn, exportPath); err != nil {
		t.Fatal(err)
	}
	if err := DumpSQLite(exportPath, dump); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(dump, restored); err != nil {
		t.Fatal(err)
	}
	final, err := gorm.Open(sqlite.Open(restored), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGorm(final)
	f.assert(t, final)
}

func TestSSHSQLiteOldSchemaUpgradePreservesExistingIdentity(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	path := filepath.Join(t.TempDir(), "old.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	row := seedMieruMigrationDatabase(t, legacy)
	for _, column := range []string{"ssh_username", "ssh_authorized_keys", "ssh_password"} {
		if err := legacy.Migrator().DropColumn(&model.ClientRecord{}, column); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Migrator().DropColumn(&model.Inbound{}, "ssh_host_key_id"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Migrator().DropTable(&model.NativeSSHHostKey{}); err != nil {
		t.Fatal(err)
	}
	closeGorm(legacy)
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	requireMieruMigrationRecord(t, GetDB(), row)
	var record model.ClientRecord
	if err := GetDB().First(&record, row.Id).Error; err != nil || record.SSHUsername != "" || record.SSHPassword != "" {
		t.Fatal("old schema upgrade changed existing identities or generated unused SSH credentials")
	}
}

func TestSSHPostgresOldSchemaUpgradePreservesExistingIdentity(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires isolated PostgreSQL destination")
	}
	cleanup, err := testpg.IsolatePackage("ssh_upgrade")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	legacy, err := gorm.Open(postgres.Open(os.Getenv("XUI_DB_DSN")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	row := seedMieruMigrationDatabase(t, legacy)
	for _, column := range []string{"ssh_username", "ssh_authorized_keys", "ssh_password"} {
		if err := legacy.Migrator().DropColumn(&model.ClientRecord{}, column); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Migrator().DropColumn(&model.Inbound{}, "ssh_host_key_id"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Migrator().DropTable(&model.NativeSSHHostKey{}); err != nil {
		t.Fatal(err)
	}
	closeGorm(legacy)
	if err := InitDB(""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	requireMieruMigrationRecord(t, GetDB(), row)
}
