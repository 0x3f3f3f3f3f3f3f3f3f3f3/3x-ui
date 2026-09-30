package service

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The resource row must be locked before canonical mutation. A concurrent
// resource rotation commits first; its credentials and unrelated fields stay.
func TestPasswordProxyOwnerFieldsPostgresConcurrentRotation(t *testing.T) {
	for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			owner, password, ordinary := passwordUpdateSibling(t)
			db := database.GetDB()
			if db.Name() != "postgres" {
				t.Skip("PostgreSQL resource row-lock test; required by PostgreSQL gate")
			}
			writer := db.Begin()
			if writer.Error != nil {
				t.Fatal(writer.Error)
			}
			t.Cleanup(func() { _ = writer.Rollback().Error })
			var locked model.Inbound
			if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, password.Id).Error; err != nil {
				t.Fatal(err)
			}
			var lockedOwner model.ClientRecord
			if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedOwner, owner.Id).Error; err != nil {
				t.Fatal(err)
			}
			request := *password
			request.Settings = passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "current-resource-secret", "ownerClientId": owner.StableID, "note": "current-resource-metadata"})
			if err := preparePasswordProxyOwnerCommand(&request); err != nil {
				t.Fatal(err)
			}
			if err := writer.Model(&model.Inbound{}).Where("id = ?", password.Id).Update("settings", request.Settings).Error; err != nil {
				t.Fatal(err)
			}
			if err := writer.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Updates(map[string]any{
				"enable": false, "password": "current-shared-secret", "limit_ip": 7, "total_gb": 7777,
				"expiry_time": int64(1800000000000), "policy_multiplier": "0.25",
			}).Error; err != nil {
				t.Fatal(err)
			}
			var settings map[string]any
			if err := json.Unmarshal([]byte(ordinary.Settings), &settings); err != nil {
				t.Fatal(err)
			}
			entry := settings["clients"].([]any)[0].(map[string]any)
			entry["id"], entry["enable"], entry["limitIp"], entry["totalGB"], entry["expiryTime"] = "edd70169-e480-42a1-a9d0-dbc4a8a6b108", false, 7, 7777, int64(1800000000000)
			encoded, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Model(&model.Inbound{}).Where("id = ?", ordinary.Id).Update("settings", string(encoded)).Error; err != nil {
				t.Fatal(err)
			}
			reached := make(chan struct{})
			var once sync.Once
			const callback = "test-password-fields-held-resource"
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table == "inbounds" && query.Statement.Clauses["FOR"].Expression != nil {
					once.Do(func() { close(reached) })
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			result := make(chan error, 1)
			go func() {
				result <- passwordOwnerFieldOperation(&ClientService{}, &InboundService{}, owner.Email, operation)
			}()
			select {
			case <-reached:
			case err := <-result:
				t.Fatalf("field update did not attempt held resource lock: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("field update did not attempt resource lock")
			}
			select {
			case err := <-result:
				t.Fatalf("field update bypassed held resource lock: %v", err)
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
				t.Fatal("field update did not finish after resource rotation committed")
			}
			if saved, err := (&InboundService{}).GetInbound(password.Id); err != nil || saved.Settings != request.Settings {
				t.Fatalf("field update overwrote resource credentials: %+v/%v", saved, err)
			}
			current, err := (&ClientService{}).GetByID(owner.Id)
			wantEnable, wantLimit, wantTotal, wantExpiry := false, 7, int64(7777), int64(1800000000000)
			switch operation {
			case "toggle":
				wantEnable = true
			case "ip-limit":
				wantLimit = 3
			case "quota":
				wantTotal = 2147483648
			case "expiry":
				wantExpiry = 1900000000000
			}
			if err != nil || current.Password != "current-shared-secret" || current.Enable != wantEnable || current.LimitIP != wantLimit || current.TotalGB != wantTotal || current.ExpiryTime != wantExpiry || current.Policy == nil || current.Policy.Multiplier != "0.25" {
				t.Fatalf("field update replayed stale canonical fields: %+v/%v", current, err)
			}
			wire, err := (&InboundService{}).GetInbound(ordinary.Id)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(wire.Settings), &settings); err != nil {
				t.Fatal(err)
			}
			entry = settings["clients"].([]any)[0].(map[string]any)
			if entry["id"] != "edd70169-e480-42a1-a9d0-dbc4a8a6b108" || entry["enable"] != wantEnable || entry["limitIp"] != float64(wantLimit) || entry["totalGB"] != float64(wantTotal) || entry["expiryTime"] != float64(wantExpiry) {
				t.Fatalf("field update replayed stale resource fields: %+v", entry)
			}
			if history := trafficOf(t, owner.Email); history.Up != 123 || history.Down != 456 {
				t.Fatalf("field update changed counters: %+v", history)
			}
		})
	}
}
