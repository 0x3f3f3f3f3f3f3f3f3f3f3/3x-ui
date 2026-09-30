package service

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestPasswordProxyOwnerRemovalPostgresCurrentAccounts(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	if db.Name() != "postgres" {
		t.Skip("PostgreSQL row-lock regression; required by the explicit PostgreSQL gate")
	}
	first, second := passwordOwner(t, "removal-locked-first"), passwordOwner(t, "removal-locked-second")
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24539, Settings: passwordOwnerSettings(t, model.HTTP,
		map[string]any{"user": "first", "pass": "old-first", "ownerClientId": first.StableID},
		map[string]any{"user": "second", "pass": "old-second", "ownerClientId": second.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	writer := db.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	t.Cleanup(func() { _ = writer.Rollback().Error })
	var locked model.Inbound
	if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	retained := map[string]any{"user": "second", "pass": "rotated-second", "ownerClientId": second.StableID, "note": "concurrent metadata"}
	current := passwordOwnerSettings(t, model.HTTP,
		map[string]any{"user": "first", "pass": "old-first", "ownerClientId": first.StableID},
		map[string]any{"user": "new-alias", "pass": "new-first", "ownerClientId": first.StableID}, retained)
	if err := writer.Model(&model.Inbound{}).Where("id = ?", ib.Id).Update("settings", current).Error; err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{})
	var once sync.Once
	const callback = "test-password-removal-current-row-lock"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "inbounds" && tx.Statement.Clauses["FOR"].Expression != nil {
			once.Do(func() { close(reached) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	result := make(chan error, 1)
	go func() {
		_, err := (&ClientService{}).Detach(svc, first.Id, []int{ib.Id})
		result <- err
	}()
	select {
	case <-reached:
	case err := <-result:
		t.Fatalf("removal did not reach the row lock: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("removal did not attempt the current row lock")
	}
	select {
	case err := <-result:
		t.Fatalf("removal bypassed held account lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := writer.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("removal did not finish after writer committed")
	}
	saved, err := svc.GetInbound(ib.Id)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(saved.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	if accounts, ok := settings["accounts"].([]any); !ok || len(accounts) != 1 || !reflect.DeepEqual(accounts[0], retained) {
		t.Fatalf("removal overwrote concurrent account changes: %s", saved.Settings)
	}
	if links := linksOf(t, ib.Id); len(links) != 1 || links[second.Id].ClientId != second.Id {
		t.Fatalf("latest account membership did not settle: %+v", links)
	}
}
