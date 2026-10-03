package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

func TestManagedAuthorityRenewalUnchangedOmissionCanRenewLater(t *testing.T) {
	for _, kind := range []string{"exact-expiry", "calendar-normalization"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			owner := managedAuthorityForProcess(process)
			boundary := time.Now().UTC().AddDate(0, 0, 1)
			boundary = time.Date(boundary.Year(), boundary.Month(), boundary.Day(), 0, 0, 0, 0, time.UTC)
			expiry, firstAt := boundary.UnixMilli(), boundary.UnixMilli()
			updates := map[string]any{"expiry_time": expiry, "reset": 1, "reset_max": 2}
			if kind == "calendar-normalization" {
				expiry, firstAt = boundary.UnixMilli()-500, boundary.UnixMilli()-250
				updates["expiry_time"], updates["reset"], updates["reset_day"] = expiry, 0, boundary.Day()
			}
			if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", client.StableID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			if err := applyAuthorityClientRenewalBatch(context.Background(), process, []string{client.StableID}, firstAt, time.UTC); err != nil {
				t.Fatal(err)
			}
			headers, err := owner.state.Journal.ResetOperationPage("", 128)
			if err != nil || len(headers) != 1 {
				t.Fatalf("first capture %v/%v", headers, err)
			}
			capture, err := owner.state.Journal.LookupResetOperation(headers[0].RequestID)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := decodeAuthorityRenewalPreparation(prepared, capture, owner.state.Journal, owner.state.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Effects) != 0 || len(snapshot.OmittedIDs) != 1 {
				t.Fatalf("fixture failed to create unchanged no-op: %+v", snapshot)
			}
			for i := 1; i <= 2; i++ {
				if err := resumeAuthorityClientRenewals(context.Background(), process); err != nil {
					t.Fatal(err)
				}
				if err := applyAuthorityClientRenewalBatch(context.Background(), process, []string{client.StableID}, boundary.UnixMilli()+int64(i)*1000, time.UTC); err != nil {
					t.Fatal(err)
				}
			}
			row := trafficOf(t, client.Email)
			var current model.ClientRecord
			if err := database.GetDB().First(&current, "stable_id = ?", client.StableID).Error; err != nil {
				t.Fatal(err)
			}
			if row.ResetCount != 1 || current.ExpiryTime <= boundary.UnixMilli() {
				t.Fatalf("unchanged no-op permanently suppresses later due renewal: canonical-expiry=%d original=%d count=%d effects=%d omitted=%v", current.ExpiryTime, expiry, row.ResetCount, len(snapshot.Effects), snapshot.OmittedIDs)
			}
		})
	}
}

func TestManagedAuthorityRenewalOmissionSurvivesCohortChanges(t *testing.T) {
	svc, _, first, target := setupManagedActivationService(t)
	db := database.GetDB()
	second := model.ClientRecord{Email: "renewal-cohort", SubID: "renewal-cohort-sub", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: second.Email, Enable: true, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	inbound := mkInbound(t, port, model.Tunnel, fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, target))
	if _, err := (&ClientService{}).Attach(&InboundService{}, second.Id, []int{inbound.Id}); err != nil {
		t.Fatal(err)
	}
	if err := (&SettingService{}).saveSetting("timeLocation", "UTC"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	past := setManagedRenewalDue(t, first, 0, 2)
	if err := db.Model(&second).Updates(map[string]any{"expiry_time": past, "reset": 1, "reset_max": 2}).Error; err != nil {
		t.Fatal(err)
	}
	ids := []string{first.StableID, second.StableID}
	var capture *policyauthority.ResetOperationCapture
	lock.Lock()
	err = runSerializedTxContextForDatabase(context.Background(), db, func(tx *gorm.DB) error {
		triggers, err := selectAuthorityRenewalTriggersTx(tx, ids, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		capture, err = captureAuthorityClientRenewalTx(tx, owner.state, triggers, time.Now().UnixMilli(), time.UTC)
		return err
	})
	lock.Unlock()
	if err != nil || capture == nil {
		t.Fatalf("cohort capture: %v", err)
	}
	if err := db.Model(&model.ClientRecord{}).Where("stable_id IN ?", ids).Update("expiry_time", past+86400000).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	prepared, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := decodeAuthorityRenewalCapture(*capture, owner.state.Journal, owner.state.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := decodeAuthorityRenewalPreparation(prepared, *capture, owner.state.Journal, owner.state.SourceID)
	if err != nil || len(empty.Effects) != 0 || len(empty.OmittedIDs) != 2 {
		t.Fatalf("not an omitted cohort: %+v/%v", empty, err)
	}
	for _, client := range []*model.ClientRecord{first, &second} {
		if err := db.Model(client).Update("expiry_time", past).Error; err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, client := range []*model.ClientRecord{first, &second} {
		row := trafficOf(t, client.Email)
		if row.ResetCount != 1 || row.ExpiryTime != past+86400000 {
			t.Fatalf("changed cohort spent or lost allowance: %+v", row)
		}
		var resets []model.ClientPolicyReset
		if err := db.Where("client_id = ?", client.StableID).Find(&resets).Error; err != nil {
			t.Fatal(err)
		}
		trigger := original.Triggers[slices.IndexFunc(original.Triggers, func(v authorityRenewalTrigger) bool { return v.ClientID == client.StableID })]
		if len(resets) != 1 || resets[0].RequestID != authorityRenewalClientRequest(owner.state.SourceID, original.Zone, trigger) {
			t.Fatalf("cohort changed original client request: %+v", resets)
		}
	}
	unchanged, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
	if err != nil || unchanged != prepared {
		t.Fatalf("cohort predecessor changed: %v", err)
	}
}

func TestAuthorityRenewalSuccessorRequiresCompletedOmission(t *testing.T) {
	for _, kind := range []string{"valid", "missing-capture", "missing-preparation", "missing-completion", "prepared-effect", "wrong-schema"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			owner := managedAuthorityForProcess(process)
			boundary := time.Now().UTC().AddDate(0, 0, 1).UnixMilli()
			if kind == "prepared-effect" {
				boundary = time.Now().Add(-time.Hour).UnixMilli()
			}
			if err := database.GetDB().Model(client).Updates(map[string]any{"expiry_time": boundary, "reset": 1, "reset_max": 2}).Error; err != nil {
				t.Fatal(err)
			}
			var capture *policyauthority.ResetOperationCapture
			lock.Lock()
			err := runSerializedTxContextForDatabase(context.Background(), database.GetDB(), func(tx *gorm.DB) error {
				triggers, err := selectAuthorityRenewalTriggersTx(tx, []string{client.StableID}, boundary)
				if err != nil {
					return err
				}
				if kind == "missing-capture" {
					original := authorityRenewalCaptureSnapshot{Schema: 1, At: boundary, Zone: "UTC", Triggers: triggers}
					raw, _ := json.Marshal(original)
					capture = &policyauthority.ResetOperationCapture{Identity: owner.state.Journal.Identity(), SourceID: owner.state.SourceID, RequestID: authorityRenewalKey(owner.state.SourceID, "UTC", triggers), Snapshot: string(raw)}
					return nil
				}
				capture, err = captureAuthorityClientRenewalTx(tx, owner.state, triggers, boundary, time.UTC)
				return err
			})
			lock.Unlock()
			if err != nil || capture == nil {
				t.Fatalf("predecessor fixture: %v", err)
			}
			original, err := decodeAuthorityRenewalCaptureEnvelope(*capture, owner.state.Journal, owner.state.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "valid", "wrong-schema", "prepared-effect":
				if err := applyAuthorityClientRenewalBatch(context.Background(), process, []string{client.StableID}, boundary, time.UTC); err != nil {
					t.Fatal(err)
				}
			case "missing-completion":
				snapshot := authorityRenewalPreparationSnapshot{Schema: 1, RequestID: capture.RequestID, ResetAt: boundary, OmittedIDs: []string{client.StableID}}
				raw, _ := json.Marshal(snapshot)
				if err := owner.state.Journal.PrepareResetOperation(policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: string(raw)}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			original.Schema, original.Generation, original.At = 2, 1, boundary+1000
			if kind == "wrong-schema" {
				original.Schema = 1
			}
			raw, _ := json.Marshal(original)
			next := policyauthority.ResetOperationCapture{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: authorityRenewalGenerationKey(owner.state.SourceID, original.Zone, original.Triggers, 1), Snapshot: string(raw)}
			_, err = decodeAuthorityRenewalCapture(next, owner.state.Journal, owner.state.SourceID)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("%s successor validation: %v", kind, err)
			}
			after, readErr := owner.state.Journal.Account(client.StableID)
			if readErr != nil || before != after {
				t.Fatalf("successor validation changed full account: %v", readErr)
			}
		})
	}
}

func TestManagedAuthorityRenewalRevertedTriggerCanRenew(t *testing.T) {
	for _, kind := range []string{"expiry-restored", "configuration-restored"} {
		t.Run(kind, func(t *testing.T) {
			svc, inbound, client, _ := setupManagedActivationService(t)
			if err := (&SettingService{}).saveSetting("timeLocation", "UTC"); err != nil {
				t.Fatal(err)
			}
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
			var configured conf.ClientPolicyConfig
			if err := json.Unmarshal(process.GetConfig().ClientPolicy, &configured); err != nil {
				t.Fatal(err)
			}
			past := setManagedRenewalDue(t, client, 0, 2)
			var capture *policyauthority.ResetOperationCapture
			lock.Lock()
			err = runSerializedTxContextForDatabase(context.Background(), database.GetDB(), func(tx *gorm.DB) error {
				triggers, err := selectAuthorityRenewalTriggersTx(tx, []string{client.StableID}, time.Now().UnixMilli())
				if err != nil {
					return err
				}
				capture, err = captureAuthorityClientRenewalTx(tx, owner.state, triggers, time.Now().UnixMilli(), time.UTC)
				return err
			})
			lock.Unlock()
			if err != nil || capture == nil {
				t.Fatalf("capture: %v", err)
			}
			if kind == "expiry-restored" {
				if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", client.StableID).Update("expiry_time", past+86400000).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				// Controlled process snapshot admission, not physical native reattachment.
				without := configured
				without.Policies = slices.DeleteFunc(slices.Clone(configured.Policies), func(policy clientpolicy.Policy) bool { return policy.ClientID == client.StableID })
				raw, err := json.Marshal(without)
				if err != nil {
					t.Fatal(err)
				}
				next := *process.GetConfig()
				next.ClientPolicy = raw
				process.SetConfig(&next)
			}
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			prepared, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := decodeAuthorityRenewalPreparation(prepared, *capture, owner.state.Journal, owner.state.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Effects) != 0 || len(snapshot.OmittedIDs) != 1 {
				t.Fatalf("not omitted: %+v", snapshot)
			}
			if kind == "expiry-restored" {
				if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", client.StableID).Update("expiry_time", past).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				raw, err := json.Marshal(configured)
				if err != nil {
					t.Fatal(err)
				}
				next := *process.GetConfig()
				next.ClientPolicy = raw
				process.SetConfig(&next)
			}
			for i := 0; i < 2; i++ {
				if _, _, err := svc.GetXrayTraffic(); err != nil {
					t.Fatal(err)
				}
			}
			managedActivationEcho(t, flow, "next")
			var current model.ClientRecord
			if err := database.GetDB().First(&current, "stable_id = ?", client.StableID).Error; err != nil {
				t.Fatal(err)
			}
			managedActivationEcho(t, flow, "stay")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			row := trafficOf(t, client.Email)
			if row.ResetCount != 1 || current.ExpiryTime <= time.Now().UnixMilli() {
				t.Fatalf("ordinary public polls cannot renew legitimately reverted trigger: expiry=%d original=%d count=%d", current.ExpiryTime, past, row.ResetCount)
			}
			unchanged, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
			if err != nil || unchanged != prepared {
				t.Fatalf("omission predecessor changed: %v", err)
			}
			original, err := decodeAuthorityRenewalCapture(*capture, owner.state.Journal, owner.state.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			var resets []model.ClientPolicyReset
			if err := database.GetDB().Where("client_id = ?", client.StableID).Find(&resets).Error; err != nil {
				t.Fatal(err)
			}
			if len(resets) != 1 || resets[0].RequestID != authorityRenewalClientRequest(owner.state.SourceID, original.Zone, original.Triggers[0]) {
				t.Fatalf("successor lost original semantic request: %+v", resets)
			}
			account, err := owner.state.Journal.Account(client.StableID)
			if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 112, RawDownload: 212, BilledBytes: 348}) || account.WindowBaseline != 316 || account.WindowUsed != 32 {
				t.Fatalf("successor changed actual2x lifetime/window: %+v/%v", account, err)
			}
			key := authorityRenewalGenerationKey(owner.state.SourceID, original.Zone, original.Triggers, 1)
			nextCapture, err := owner.state.Journal.LookupResetOperation(key)
			if err != nil {
				t.Fatal(err)
			}
			nextPreparation, err := owner.state.Journal.LookupResetPreparation(key)
			if err != nil {
				t.Fatal(err)
			}
			nextEffect, err := decodeAuthorityRenewalPreparation(nextPreparation, nextCapture, owner.state.Journal, owner.state.SourceID)
			if err != nil || len(nextEffect.Effects) != 1 {
				t.Fatalf("missing exact successor effect: %+v/%v", nextEffect, err)
			}
			effect := nextEffect.Effects[0]
			stateFile := owner.config.StateFile
			if err := stopManagedProcess(context.Background(), process); err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Table("clients").Where("stable_id = ?", client.StableID).Updates(map[string]any{"expiry_time": effect.BeforeExpiryTime, "updated_at": effect.BeforeUpdatedAt, "desired_policy_version": effect.BeforePolicyVersion, "policy_fingerprint": effect.BeforePolicyFingerprint}).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(&row).Updates(map[string]any{"expiry_time": effect.BeforeExpiryTime, "reset_count": effect.BeforeResetCount}).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Where("client_id = ? AND request_id = ?", client.StableID, resets[0].RequestID).Delete(&model.ClientPolicyReset{}).Error; err != nil {
				t.Fatal(err)
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(stateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			before, err := state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			err = recoverAuthorityResetCaptures(context.Background(), database.GetDB(), state.Journal, state.SourceID)
			after, readErr := state.Journal.Account(client.StableID)
			closeErr := state.Journal.Close()
			if err != nil || readErr != nil || closeErr != nil || before != after {
				t.Fatalf("successor cold metadata changed account: %v/%v/%v", err, readErr, closeErr)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			if trafficOf(t, client.Email).ResetCount != 1 {
				t.Fatal("cold successor recovery spent another allowance")
			}

		})
	}
}
