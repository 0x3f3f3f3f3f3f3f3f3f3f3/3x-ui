package service

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestDatabaseRestoreConcurrentImportKeepsFirstStagedDatabase(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	setupPolicyLedgerDB(t)
	if err := database.GetDB().Create(&model.Setting{Key: "concurrent-restore-marker", Value: "original"}).Error; err != nil {
		t.Fatal(err)
	}
	payloads := make(map[string][]byte)
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(t.TempDir(), name+".db")
		if err := database.BackupSQLite(path); err != nil {
			t.Fatal(err)
		}
		pool, err := sql.Open("sqlite3", path)
		if err != nil {
			t.Fatal(err)
		}
		_, updateErr := pool.Exec("UPDATE settings SET value = ? WHERE key = 'concurrent-restore-marker'", name)
		closeErr := pool.Close()
		if updateErr != nil || closeErr != nil {
			t.Fatalf("prepare fixture: update=%v close=%v", updateErr, closeErr)
		}
		payloads[name], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	oldStop, oldRestart := stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore
	oldManual := isManuallyStopped.Load()
	t.Cleanup(func() {
		stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore = oldStop, oldRestart
		isManuallyStopped.Store(oldManual)
	})
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var stops, restarts atomic.Int32
	stopXrayBeforeDatabaseRestore = func(*ServerService) error {
		if stops.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	}
	restartXrayAfterDatabaseRestore = func(*ServerService, *databaseRestoreOwner) error { restarts.Add(1); return nil }
	first := make(chan error, 1)
	go func() { first <- (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(payloads["first"])}, false) }()
	waitTrafficWriterSignal(t, started, "first import did not stage its database")
	secondErr := (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(payloads["second"])}, false)
	if secondErr == nil || !strings.Contains(secondErr.Error(), "restore is already in progress") {
		t.Errorf("concurrent import was not rejected before staging: %v", secondErr)
	}
	releaseOnce.Do(func() { close(release) })
	if err := waitTrafficWriterErr(t, first); err != nil {
		t.Errorf("first import lost its staged database: %v", err)
	}
	var marker model.Setting
	if err := database.GetDB().First(&marker, "key = ?", "concurrent-restore-marker").Error; err != nil || marker.Value != "first" {
		t.Errorf("concurrent import displaced the first requested restore: marker=%q err=%v", marker.Value, err)
	}
	if stops.Load() != 1 || restarts.Load() != 1 {
		t.Errorf("concurrent restore lifecycle: stops=%d restarts=%d", stops.Load(), restarts.Load())
	}
}
