package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"gorm.io/gorm"
)

func TestAuthorityProjectionConcurrentParentLockUsesCurrentJournal(t *testing.T) {
	journal, first := authorityProjectionFixture(t)
	db := database.GetDB()
	if db.Dialector.Name() != "postgres" {
		t.Skip("requires actual PostgreSQL row-lock ordering")
	}
	boot := policyauthority.NodeBoot{NodeID: "node-b", SourceID: "source-b", BootID: "boot-b"}
	if err := journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Binding.NodeBoot, second.RequestID, second.ChallengeID, second.Capacity = boot, "second-request", "second-challenge", 20
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked := db.WithContext(context.WithValue(ctx, serializedTxContextKey{}, true)).Begin()
	if locked.Error != nil {
		t.Fatal(locked.Error)
	}
	defer locked.Rollback()
	account, err := journal.Account(first.Binding.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAuthorityProjectionClient(locked, account); err != nil {
		t.Fatal(err)
	}
	type issuerMarker struct{}
	entered, release := make(chan struct{}), make(chan struct{})
	var once, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	callback := "test:projection_second_issuer_parent_lock"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "clients" && tx.Statement.Context.Value(issuerMarker{}) == true {
			once.Do(func() { close(entered); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	done := make(chan error, 1)
	go func() {
		_, err := issueClientPolicyAuthority(context.WithValue(ctx, issuerMarker{}, true), db, journal, second)
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("second issuer never reached parent lock")
	}
	if _, err := journal.Issue(first); err != nil {
		t.Fatal(err)
	}
	if err := projectClientPolicyAuthorityTx(locked, journal, first.Binding.ClientID); err != nil {
		t.Fatal(err)
	}
	if err := locked.Commit().Error; err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("valid second issuer rejected newer committed projection: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("second issuer did not complete")
	}
	var row model.ClientPolicyAuthorityProjection
	if err := db.First(&row, "client_id = ?", first.Binding.ClientID).Error; err != nil {
		t.Fatal(err)
	}
	_, current := authorityProjection(t, first.Binding.ClientID)
	final, err := journal.Account(first.Binding.ClientID)
	if err != nil || final.HeldCapacity != 60 || current != final || row.Revision != int64(final.Revision) {
		t.Fatalf("concurrent issuance lost conservation/projection: %+v/%v", final, err)
	}
}
