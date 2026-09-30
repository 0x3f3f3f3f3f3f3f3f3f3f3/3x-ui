package service

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerSingleUpdatePostgresConcurrentRotation(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	if db.Name() != "postgres" {
		t.Skip("PostgreSQL row-lock test; required by the explicit PostgreSQL gate")
	}
	owner := passwordOwner(t, "update-locked-owner")
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24561, Settings: passwordOwnerSettings(t, model.HTTP,
		map[string]any{"user": "alice", "pass": "old-resource", "ownerClientId": owner.StableID})})
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
	var lockedOwner model.ClientRecord
	if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedOwner, owner.Id).Error; err != nil {
		t.Fatal(err)
	}
	rotated := passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "rotated-resource", "ownerClientId": owner.StableID, "note": "concurrent metadata"})
	// Preserve the protection field present on the saved resource command.
	request := *ib
	request.Settings = rotated
	if err := preparePasswordProxyOwnerCommand(&request); err != nil {
		t.Fatal(err)
	}
	rotated = request.Settings
	if err := writer.Model(&model.Inbound{}).Where("id = ?", ib.Id).Update("settings", rotated).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Update("policy_multiplier", "0.25").Error; err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{})
	var once sync.Once
	const callback = "test-password-update-held-owner-row"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "clients" && tx.Statement.Clauses["FOR"].Expression != nil {
			once.Do(func() { close(reached) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	updated := *owner.ToClient()
	updated.Comment, updated.TotalGB, updated.Policy = "shared update", 9999, nil
	result := make(chan error, 1)
	go func() { _, err := (&ClientService{}).Update(svc, owner.Id, updated, 0); result <- err }()
	select {
	case <-reached:
	case err := <-result:
		t.Fatalf("shared update did not reach held owner lock: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("shared update did not attempt canonical lock")
	}
	select {
	case err := <-result:
		t.Fatalf("shared update bypassed held owner lock: %v", err)
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
	case <-time.After(5 * time.Second):
		t.Fatal("shared update did not finish after rotation committed")
	}
	var saved model.Inbound
	if err := db.First(&saved, ib.Id).Error; err != nil || saved.Settings != rotated {
		t.Fatalf("shared update overwrote resource rotation: %+v %v", saved, err)
	}
	current, err := (&ClientService{}).GetByID(owner.Id)
	if err != nil || current.Comment != updated.Comment || current.TotalGB != updated.TotalGB || current.Policy == nil || current.Policy.Multiplier != "0.25" {
		t.Fatalf("shared update lost latest canonical fields: %+v %v", current, err)
	}
}

func TestPasswordProxyOwnerSingleUpdatePostgresDestinationReservation(t *testing.T) {
	for _, sibling := range []bool{false, true} {
		t.Run(map[bool]string{false: "password-only", true: "ordinary-sibling"}[sibling], func(t *testing.T) {
			owner, password, ordinary := passwordUpdateSibling(t)
			db := database.GetDB()
			if db.Name() != "postgres" {
				t.Skip("PostgreSQL destination race; required by the explicit PostgreSQL gate")
			}
			replacement := passwordOwner(t, "reservation-replacement")
			updated := *owner.ToClient()
			updated.Email, updated.Comment = "reservation-destination", "must-not-save"
			var changed atomic.Bool
			const callback = "test-password-update-destination-after-check"
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table != "clients" || !strings.Contains(query.Statement.SQL.String(), "count(*)") || !strings.Contains(query.Statement.SQL.String(), "email =") {
					return
				}
				if _, insideWriter := query.Statement.ConnPool.(gorm.TxCommitter); !insideWriter || !changed.CompareAndSwap(false, true) {
					return
				}
				query.AddError(db.Transaction(func(tx *gorm.DB) error {
					return tx.Model(&model.ClientRecord{}).Where("id = ?", replacement.Id).Update("email", updated.Email).Error
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			var filter []int
			if !sibling {
				filter = []int{password.Id}
				// Remove the ordinary attachment so the helper reserves directly.
				if err := db.Where("client_id = ? AND inbound_id = ?", owner.Id, ordinary.Id).Delete(&model.ClientInbound{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			_, err := (&ClientService{}).Update(&InboundService{}, owner.Id, updated, 0, filter...)
			if !changed.Load() || !errors.Is(err, ErrManagedConfigStale) {
				t.Fatalf("destination acquired after under-lock check escaped reservation: changed=%v err=%v", changed.Load(), err)
			}
			current, readErr := (&ClientService{}).GetByID(owner.Id)
			if readErr != nil || !reflect.DeepEqual(current, &owner) {
				t.Fatalf("failed reservation changed original owner: %+v %v", current, readErr)
			}
			var saved model.Inbound
			if readErr := db.First(&saved, ordinary.Id).Error; readErr != nil || saved.Settings != ordinary.Settings {
				t.Fatalf("failed reservation committed ordinary settings: %+v %v", saved, readErr)
			}
			var usage xray.ClientTraffic
			if err := db.Where("email = ?", owner.Email).First(&usage).Error; err != nil || usage.Up != 123 || usage.Down != 456 {
				t.Fatalf("failed reservation changed history: %+v %v", usage, err)
			}
		})
	}
}

func TestPasswordProxyOwnerSingleUpdatePostgresFreedLabelReuse(t *testing.T) {
	owner, _, ordinary := passwordUpdateSibling(t)
	db := database.GetDB()
	if db.Name() != "postgres" {
		t.Skip("PostgreSQL freed-label race; required by the explicit PostgreSQL gate")
	}
	other := passwordOwner(t, "freed-label-replacement")
	updated := *owner.ToClient()
	updated.Email = "freed-label-original-renamed"
	var changed atomic.Bool
	var global model.ClientGlobalTraffic
	var node model.NodeClientTraffic
	const callback = "test-password-update-freed-label-acquisition"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(query *gorm.DB) {
		if query.Statement.Table != "clients" || !strings.Contains(query.Statement.SQL.String(), "count(*)") || !strings.Contains(query.Statement.SQL.String(), "email =") {
			return
		}
		foundOld := false
		for _, value := range query.Statement.Vars {
			if value == owner.Email {
				foundOld = true
			}
		}
		if !foundOld || !changed.CompareAndSwap(false, true) {
			return
		}
		query.AddError(db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.ClientRecord{}).Where("id = ?", other.Id).Update("email", owner.Email).Error; err != nil {
				return err
			}
			global = model.ClientGlobalTraffic{MasterGuid: "replacement-master", Email: owner.Email, Up: 33, Down: 44}
			node = model.NodeClientTraffic{NodeId: 9, Email: owner.Email, Up: 55, Down: 66}
			for _, row := range []any{&global, &node} {
				if err := tx.Create(row).Error; err != nil {
					return err
				}
			}
			return nil
		}))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if _, err := (&ClientService{}).Update(&InboundService{}, owner.Id, updated, 0); err != nil {
		t.Fatal(err)
	}
	if !changed.Load() {
		t.Fatal("freed label interleaving did not run")
	}
	var gotGlobal model.ClientGlobalTraffic
	var gotNode model.NodeClientTraffic
	if err := db.First(&gotGlobal, global.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&gotNode, node.Id).Error; err != nil {
		t.Fatal(err)
	}
	if gotGlobal.Email != owner.Email || gotGlobal.Up != 33 || gotGlobal.Down != 44 || gotNode.Email != owner.Email || gotNode.Up != 55 || gotNode.Down != 66 {
		t.Fatalf("later transaction swept a replacement's freed-label history: global=%+v node=%+v", gotGlobal, gotNode)
	}
	var saved model.Inbound
	if err := db.First(&saved, ordinary.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.Settings, updated.Email) {
		t.Fatal("original ordinary rename was not committed")
	}
}
