package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestDatabaseRestoreSQLLeaseIsExplicitAndExpires(t *testing.T) {
	setupPolicyLedgerDB(t)
	lease, err := database.BeginRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	db := database.GetDB()
	owned := db.WithContext(lease.Context(context.Background()))
	if err := owned.Create(&model.Setting{Key: "owner-write", Value: "permitted"}).Error; err != nil {
		t.Fatalf("explicit restore owner could not reconcile SQL: %v", err)
	}
	var value string
	if err := db.Raw("SELECT value FROM settings WHERE key = ?", "owner-write").Row().Scan(&value); !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("row query bypassed restore admission: %v", err)
	}
	lease.Close()
	second, err := database.BeginRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := owned.Create(&model.Setting{Key: "stale-owner", Value: "forbidden"}).Error; !errors.Is(err, database.ErrRestoreLeaseExpired) {
		t.Fatalf("retired SQL context could mutate a subsequent import: %v", err)
	}
	lease.Close()
	if err := db.Create(&model.Setting{Key: "unowned-new-import", Value: "forbidden"}).Error; !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("retired lease release removed a newer import's SQL fence: %v", err)
	}
	second.Close()
	if err := db.Raw("SELECT value FROM settings WHERE key = ?", "owner-write").Row().Scan(&value); err != nil || value != "permitted" {
		t.Fatalf("successful owner history unavailable after fence: %q/%v", value, err)
	}
}

func TestDatabaseRestoreSQLLeaseReleaseJoinsOwnedTransaction(t *testing.T) {
	setupPolicyLedgerDB(t)
	lease, err := database.BeginRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	tx := database.GetDB().WithContext(lease.Context(context.Background())).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Create(&model.Setting{Key: "owned-transaction", Value: "committed"}).Error; err != nil {
		t.Fatal(err)
	}
	closing, closed := make(chan struct{}), make(chan error, 1)
	go func() { close(closing); lease.Close(); closed <- nil }()
	<-closing
	select {
	case <-closed:
		t.Fatal("normal SQL admission reopened before the owned transaction committed")
	case <-time.After(250 * time.Millisecond):
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := waitTrafficWriterErr(t, closed); err != nil {
		t.Fatal(err)
	}
}
