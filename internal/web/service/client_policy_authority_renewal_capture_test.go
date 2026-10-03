package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

func TestManagedAuthorityRenewalCapturedIntentResumesCurrentDesiredState(t *testing.T) {
	for _, kind := range []string{"original", "unrelated-policy", "changed-trigger", "renamed", "deleted"} {
		t.Run(kind, func(t *testing.T) {
			svc, inbound, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			owner := managedAuthorityForProcess(process)
			flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			managedActivationEcho(t, flow, "warm")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			past := setManagedRenewalDue(t, client, 0, 2)
			var capture *policyauthority.ResetOperationCapture
			capturedAt := time.Now().Add(-5 * time.Second).UnixMilli()
			lock.Lock()
			err = runSerializedTxContextForDatabase(ctx, database.GetDB(), func(tx *gorm.DB) error {
				triggers, err := selectAuthorityRenewalTriggersTx(tx, []string{client.StableID}, capturedAt)
				if err != nil {
					return err
				}
				capture, err = captureAuthorityClientRenewalTx(tx, owner.state, triggers, capturedAt, time.UTC)
				return err
			})
			lock.Unlock()
			if err != nil || capture == nil {
				t.Fatalf("capture-only intent missing: %v", err)
			}
			if _, err := owner.state.Journal.LookupResetPreparation(capture.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("intent already has effects: %v", err)
			}
			db := database.GetDB()
			switch kind {
			case "unrelated-policy":
				if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", client.StableID).Updates(&model.ClientRecord{Policy: &model.ClientPolicyOptions{Multiplier: "3", UploadBytesPerSecond: 32768}}).Error; err != nil {
					t.Fatal(err)
				}
			case "changed-trigger":
				if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", client.StableID).Updates(map[string]any{"expiry_time": past + 2*86400000, "reset": 2}).Error; err != nil {
					t.Fatal(err)
				}
			case "renamed":
				if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", client.StableID).Update("email", "renamed-renewal-owner").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("email", "renamed-renewal-owner").Error; err != nil {
					t.Fatal(err)
				}
				client.Email = "renamed-renewal-owner"
				var current model.Inbound
				if err := db.First(&current, inbound.Id).Error; err != nil {
					t.Fatal(err)
				}
				var settings map[string]json.RawMessage
				var aliases []map[string]json.RawMessage
				if json.Unmarshal([]byte(current.Settings), &settings) != nil || json.Unmarshal(settings["clients"], &aliases) != nil || len(aliases) != 1 {
					t.Fatal("renamed snapshot missing original mirror")
				}
				aliases[0]["email"], _ = json.Marshal(client.Email)
				settings["clients"], _ = json.Marshal(aliases)
				raw, _ := json.Marshal(settings)
				if err := db.Model(&current).Update("settings", string(raw)).Error; err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := runSerializedTx(func(tx *gorm.DB) error {
					if err := recordClientPolicyTombstones(tx, []int{client.Id}); err != nil {
						return err
					}
					return tx.Delete(&model.ClientRecord{}, "stable_id = ?", client.StableID).Error
				}); err != nil {
					t.Fatal(err)
				}
				if err := reconcileDeletedClientPolicies([]string{client.StableID}); err != nil {
					t.Fatal(err)
				}
			}
			preparedAfter := time.Now().UnixMilli()
			if err := renewLocalClientPolicies(ctx, process); err != nil {
				t.Fatal(err)
			}
			retained, err := owner.state.Journal.LookupResetOperation(capture.RequestID)
			if err != nil || retained != *capture {
				t.Fatalf("retry changed captured intent: %v", err)
			}
			prepared, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := decodeAuthorityRenewalPreparation(prepared, *capture, owner.state.Journal, owner.state.SourceID)
			if err != nil || snapshot.ResetAt < preparedAfter {
				t.Fatalf("intent preparation invented original earlier effects: %+v/%v", snapshot, err)
			}
			if _, err := owner.state.Journal.LookupResetCompletion(capture.RequestID); err != nil {
				t.Fatal(err)
			}
			if kind == "changed-trigger" || kind == "deleted" {
				if len(snapshot.OmittedIDs) != 1 || snapshot.OmittedIDs[0] != client.StableID || len(snapshot.Effects) != 0 || len(snapshot.Resets) != 0 {
					t.Fatalf("superseded intent spent original allowance: %+v", snapshot)
				}
				if row := trafficOf(t, client.Email); row.ResetCount != 0 {
					t.Fatalf("omitted original owner acquired count: %+v", row)
				}
				return
			}
			if len(snapshot.Effects) != 1 || len(snapshot.Resets) != 1 || snapshot.Resets[0].BilledBytes != 316 || snapshot.Resets[0].CreatedAt != snapshot.ResetAt {
				t.Fatalf("captured retry lost first actual boundary: %+v", snapshot)
			}
			if row := trafficOf(t, client.Email); row.ExpiryTime != past+86400000 || row.ResetCount != 1 {
				t.Fatalf("retry substituted email/expiry/count: %+v", row)
			}
			managedActivationEcho(t, flow, "next")
			if err := renewLocalClientPolicies(ctx, process); err != nil {
				t.Fatal(err)
			}
			managedActivationEcho(t, flow, "stay")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			want := uint64(348)
			if kind == "unrelated-policy" {
				want = 364
			}
			account, err := owner.state.Journal.Account(client.StableID)
			if err != nil || account.Usage.BilledBytes != want || account.WindowBaseline != 316 || account.WindowUsed != want-316 {
				t.Fatalf("intent retry lost current multiplier/original window: %+v/%v", account, err)
			}
			page, err := owner.state.Journal.ResetOperationPage("", 128)
			if err != nil || len(page) != 1 {
				t.Fatalf("retry created overlapping renewal: %d/%v", len(page), err)
			}
		})
	}
}
