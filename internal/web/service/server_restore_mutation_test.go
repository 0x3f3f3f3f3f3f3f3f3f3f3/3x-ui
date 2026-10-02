package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
)

func TestDatabaseRestoreRejectsUnownedDatabaseMutations(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	setupPolicyLedgerDB(t)
	before := database.GetDB()
	incoming := filepath.Join(t.TempDir(), "incoming.db")
	if err := database.BackupSQLite(incoming); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(incoming)
	if err != nil {
		t.Fatal(err)
	}
	oldStop, oldRestart := stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore
	t.Cleanup(func() { stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore = oldStop, oldRestart })
	stopped, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	stopXrayBeforeDatabaseRestore = func(*ServerService) error { close(stopped); <-release; return nil }
	restartXrayAfterDatabaseRestore = func(*ServerService, *databaseRestoreOwner) error { return nil }
	imported := make(chan error, 1)
	go func() { imported <- (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(payload)}, false) }()
	waitTrafficWriterSignal(t, stopped, "restore did not fence before stopping core")
	for _, write := range []struct {
		name  string
		apply func() error
	}{
		{"saved-handle-create", func() error { return before.Create(&model.Setting{Key: "during-import", Value: "external"}).Error }},
		{"raw-write", func() error {
			return before.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "during-import-raw", "external").Error
		}},
		{"serialized-write", func() error {
			return runSerializedTx(func(tx *gorm.DB) error {
				return tx.Create(&model.Setting{Key: "during-import-ledger", Value: "external"}).Error
			})
		}},
	} {
		t.Run(write.name, func(t *testing.T) {
			if err := write.apply(); !errors.Is(err, ErrDatabaseRestoreInProgress) {
				t.Errorf("unowned operation entered the restoring database: %v", err)
			}
		})
	}
	once.Do(func() { close(release) })
	if err := waitTrafficWriterErr(t, imported); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.Setting{Key: "after-import", Value: "permitted"}).Error; err != nil {
		t.Fatalf("completed restore did not reopen normal SQL admission: %v", err)
	}
}
