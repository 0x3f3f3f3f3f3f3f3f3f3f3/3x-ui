package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestManagedAuthorityRenewalRecoveryUsesEffectOrderInsteadOfHashOrder(t *testing.T) {
	for _, clock := range []string{"forward", "backward"} {
		t.Run(clock, func(t *testing.T) {
			svc, _, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			owner := managedAuthorityForProcess(process)
			cfg := owner.config
			past := time.Now().Add(-3*24*time.Hour - time.Hour).UnixMilli()
			var firstKey, secondKey string
			for range 10000 {
				first := authorityRenewalTrigger{ClientID: client.StableID, ExpiryTime: past, Reset: 1, ResetMax: 1}
				second := authorityRenewalTrigger{ClientID: client.StableID, ExpiryTime: past + 86400000, ResetCount: 1, Reset: 1, ResetMax: 2}
				firstKey = authorityRenewalKey(cfg.InstanceID, "UTC", []authorityRenewalTrigger{first})
				secondKey = authorityRenewalKey(cfg.InstanceID, "UTC", []authorityRenewalTrigger{second})
				if secondKey < firstKey {
					break
				}
				past--
			}
			if secondKey >= firstKey {
				t.Fatal("fixture could not place later effect before earlier header")
			}
			if err := (&SettingService{}).saveSetting("timeLocation", "UTC"); err != nil {
				t.Fatal(err)
			}
			db := database.GetDB()
			if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", client.StableID).Updates(map[string]any{"expiry_time": past, "reset": 1, "reset_max": 1}).Error; err != nil {
				t.Fatal(err)
			}
			var before model.ClientRecord
			if err := db.First(&before, "stable_id = ?", client.StableID).Error; err != nil {
				t.Fatal(err)
			}
			var firstErr error
			if clock == "backward" {
				firstErr = applyAuthorityClientRenewalBatch(context.Background(), process, []string{client.StableID}, time.Now().Add(time.Hour).UnixMilli(), time.UTC)
			} else {
				firstErr = renewLocalClientPolicies(context.Background(), process)
			}
			if firstErr != nil {
				t.Fatal(firstErr)
			}
			var firstAfter model.ClientRecord
			if err := db.First(&firstAfter, "stable_id = ?", client.StableID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Table("clients").Where("stable_id = ?", client.StableID).Updates(map[string]any{"reset_max": 2, "updated_at": firstAfter.UpdatedAt + 1}).Error; err != nil {
				t.Fatal(err)
			}
			if err := renewLocalClientPolicies(context.Background(), process); err != nil {
				t.Fatal(err)
			}
			if row := trafficOf(t, client.Email); row.ResetCount != 2 || row.ExpiryTime != past+2*86400000 {
				t.Fatalf("fixture did not execute both expiry-only effects: %+v", row)
			}
			page, err := owner.state.Journal.ResetOperationPage("", 128)
			if err != nil || len(page) != 2 || page[0].RequestID != secondKey || page[1].RequestID != firstKey {
				t.Fatalf("fixture did not reverse actual effect/header order: %+v/%v", page, err)
			}
			if err := stopManagedProcess(context.Background(), process); err != nil {
				t.Fatal(err)
			}
			if err := db.Table("clients").Where("stable_id = ?", client.StableID).Updates(map[string]any{"expiry_time": past, "reset_max": 1, "updated_at": before.UpdatedAt, "desired_policy_version": before.DesiredPolicyVersion, "policy_fingerprint": before.PolicyFingerprint}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Updates(map[string]any{"expiry_time": past, "reset_count": 0}).Error; err != nil {
				t.Fatal(err)
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			account, err := state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			if err := recoverAuthorityDesiredState(context.Background(), &cfg); err != nil {
				t.Fatalf("header hash order replaced original renewal effect order: %v", err)
			}
			row := trafficOf(t, client.Email)
			var current model.ClientRecord
			if err := db.First(&current, "stable_id = ?", client.StableID).Error; err != nil {
				t.Fatal(err)
			}
			if current.ExpiryTime != past+2*86400000 || row.ExpiryTime != current.ExpiryTime || row.ResetCount != 2 || current.ResetMax != 1 {
				t.Fatalf("ordered recovery lost protected effects or desired rule: %d/%+v cap=%d", current.ExpiryTime, row, current.ResetMax)
			}
			state, err = openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			after, err := state.Journal.Account(client.StableID)
			if err != nil || account != after {
				t.Fatalf("ordered metadata recovery changed complete account: %v", err)
			}
		})
	}
}
