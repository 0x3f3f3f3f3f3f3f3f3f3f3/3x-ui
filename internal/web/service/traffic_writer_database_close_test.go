package service

import (
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
)

func TestTrafficWriterActiveTransactionFinishesBeforePoolClose(t *testing.T) {
	setupPolicyLedgerDB(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	transaction := make(chan error, 1)
	go func() {
		transaction <- runSerializedTx(func(tx *gorm.DB) error {
			if err := tx.Create(&model.Setting{Key: "active-cutover-marker", Value: "committed-before-close"}).Error; err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	waitTrafficWriterSignal(t, started, "active transaction did not reach its commit barrier")
	closing, closed := make(chan struct{}), make(chan error, 1)
	go func() { close(closing); closed <- database.CloseDB() }()
	<-closing
	select {
	case err := <-closed:
		releaseOnce.Do(func() { close(release) })
		_ = waitTrafficWriterErr(t, transaction)
		t.Fatalf("pool close completed before the active transaction: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	if err := waitTrafficWriterErr(t, transaction); err != nil {
		t.Fatal(err)
	}
	if err := waitTrafficWriterErr(t, closed); err != nil {
		t.Fatal(err)
	}
}
