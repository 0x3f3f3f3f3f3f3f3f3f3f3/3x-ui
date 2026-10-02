package service

import (
	"context"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestDirectTransactionFinishesBeforePoolClose(t *testing.T) {
	setupPolicyLedgerDB(t)
	tx := database.GetDB().Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Create(&model.Setting{Key: "direct-transaction", Value: "retained"}).Error; err != nil {
		t.Fatal(err)
	}
	closing, closed := make(chan struct{}), make(chan error, 1)
	go func() { close(closing); closed <- database.CloseDB() }()
	<-closing
	select {
	case err := <-closed:
		t.Fatalf("close crossed an active direct transaction: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := waitTrafficWriterErr(t, closed); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledDirectTransactionDoesNotBlockRestore(t *testing.T) {
	setupPolicyLedgerDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx := database.GetDB().WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- database.CloseDB() }()
	if err := waitTrafficWriterErr(t, closed); err != nil {
		t.Fatal(err)
	}
}
