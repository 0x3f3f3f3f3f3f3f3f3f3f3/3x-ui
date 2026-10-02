package service

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"gorm.io/gorm"
)

func TestTrafficWriterQueuedTransactionCannotCrossDatabaseReplacement(t *testing.T) {
	setupPolicyLedgerDB(t)
	resetTrafficWriterForTest(t)
	StartTrafficWriter()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- submitTrafficWrite(func() error { close(started); <-release; return nil })
	}()
	waitTrafficWriterSignal(t, started, "blocking writer did not start")
	queuedErr := make(chan error, 1)
	go func() {
		queuedErr <- runSerializedTx(func(tx *gorm.DB) error {
			return tx.Model(&model.Setting{}).Where("key = ?", "restore-generation-marker").Update("value", "old-queued-write").Error
		})
	}()
	waitTrafficWriterQueued(t)
	if database.IsPostgres() {
		cleanup, err := testpg.IsolatePackage("traffic_writer_replacement")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
	}
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := database.InitDB(filepath.Join(dir, "replacement.db")); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.Setting{Key: "restore-generation-marker", Value: "restored-state"}).Error; err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := waitTrafficWriterErr(t, firstErr); err != nil {
		t.Fatal(err)
	}
	if err := waitTrafficWriterErr(t, queuedErr); err == nil {
		t.Error("old queued transaction succeeded against the replacement database")
	}
	var marker model.Setting
	if err := database.GetDB().First(&marker, "key = ?", "restore-generation-marker").Error; err != nil || marker.Value != "restored-state" {
		t.Errorf("old transaction changed restored data: value=%q err=%v", marker.Value, err)
	}
	// Newly submitted work must still use the new database normally.
	if err := runSerializedTx(func(tx *gorm.DB) error {
		return tx.Model(&model.Setting{}).Where("key = ?", "restore-generation-marker").Update("value", "new-generation-write").Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().First(&marker, "key = ?", "restore-generation-marker").Error; err != nil || marker.Value != "new-generation-write" {
		t.Fatalf("new transaction did not commit: value=%q err=%v", marker.Value, err)
	}
}
