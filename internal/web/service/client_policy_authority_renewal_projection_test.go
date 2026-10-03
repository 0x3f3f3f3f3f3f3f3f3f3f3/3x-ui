package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

func TestManagedAuthorityRenewalMetadataPreservesLaterDesiredAndFundedState(t *testing.T) {
	for _, kind := range []string{"original", "later-expiry", "later-rules", "later-multiplier", "manual-disable", "inconsistent-tuple", "wrong-source", "cancelled", "stale-handle", "malformed-capture"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAuthorityRenewalRecoveryFixture(t, false)
			restoreAuthorityRenewalSQL(t, f)
			db := database.GetDB()
			expiry := f.snapshot.Effects[0].AfterExpiryTime
			wantErr := error(nil)
			switch kind {
			case "later-expiry":
				expiry += 86400000
				if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Updates(map[string]any{"expiry_time": expiry, "desired_policy_version": f.snapshot.Effects[0].AfterPolicyVersion + 1, "updated_at": f.snapshot.ResetAt + 1000}).Error; err != nil {
					t.Fatal(err)
				}
			case "later-rules", "later-multiplier", "manual-disable":
				updates := map[string]any{"expiry_time": expiry, "updated_at": f.snapshot.ResetAt + 1000}
				if kind == "later-rules" {
					updates["reset"] = 7
				}
				if kind == "later-multiplier" {
					updates["policy_multiplier"] = "3"
					updates["policy_upload_bytes_per_second"] = 32768
				}
				if kind == "manual-disable" {
					updates["enable"] = false
				}
				if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
			case "inconsistent-tuple":
				if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Updates(map[string]any{"desired_policy_version": f.snapshot.Effects[0].AfterPolicyVersion, "policy_fingerprint": f.snapshot.Effects[0].AfterPolicyFingerprint}).Error; err != nil {
					t.Fatal(err)
				}
				wantErr = ErrClientPolicyLedger
			case "wrong-source":
				wantErr = ErrClientPolicyLedger
			case "cancelled":
				wantErr = context.Canceled
			case "stale-handle":
				wantErr = ErrDatabaseReplaced
			case "malformed-capture":
				wantErr = ErrClientPolicyLedger
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = state.Journal.Close() })
			boot := policyauthority.NodeBoot{NodeID: "local", SourceID: f.config.InstanceID, BootID: "renewal-projection-held-boot"}
			if err := state.Journal.RegisterBoot(boot); err != nil {
				t.Fatal(err)
			}
			grant, err := state.Journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: state.Journal.Identity(), NodeBoot: boot, ClientID: f.client.StableID, WindowID: f.account.Policy.WindowID, PolicyVersion: f.account.Policy.Version}, RequestID: "renewal-projection-held40", ChallengeID: "renewal-projection-held-challenge", Capacity: 40, Upload: f.account.Policy.Upload, Download: f.account.Policy.Download, LeaseDuration: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			before, err := state.Journal.Account(f.client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "malformed-capture" {
				if err := state.Journal.CaptureResetOperation(policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: f.config.InstanceID, RequestID: authorityRenewalPrefix + "malformed-capture", Snapshot: `{}`}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			source := f.config.InstanceID
			if kind == "wrong-source" {
				source = "wrong-renewal-source"
			}
			expected := db
			if kind == "stale-handle" {
				expected = db.Session(&gorm.Session{NewDB: true})
			}
			err = recoverAuthorityResetCaptures(ctx, expected, state.Journal, source)
			if wantErr != nil {
				if !errors.Is(err, wantErr) {
					t.Fatalf("renewal %s evidence not refused: %v", kind, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, err := state.Journal.Account(f.client.StableID)
			if err != nil || before != after {
				t.Fatalf("metadata recovery changed complete funded account: %v", err)
			}
			retained, err := state.Journal.Grant(grant.GrantID)
			if err != nil || retained != grant {
				t.Fatalf("metadata recovery changed finite40 grant: %v", err)
			}
			prepared, err := state.Journal.LookupResetPreparation(f.capture.RequestID)
			if err != nil || prepared != f.prepared {
				t.Fatalf("metadata recovery changed preparation: %v", err)
			}
			if wantErr != nil {
				return
			}
			var client model.ClientRecord
			if err := db.First(&client, "stable_id = ?", f.client.StableID).Error; err != nil {
				t.Fatal(err)
			}
			row := trafficOf(t, f.client.Email)
			if client.ExpiryTime != expiry || row.ExpiryTime != expiry || row.ResetCount != 1 {
				t.Fatalf("metadata recovery lost desired expiry/original count: %d/%+v", client.ExpiryTime, row)
			}
			if kind == "later-rules" && client.Reset != 7 || kind == "manual-disable" && client.Enable || kind == "later-multiplier" && (client.Policy == nil || client.Policy.Multiplier != "3" || client.Policy.UploadBytesPerSecond != 32768) {
				t.Fatal("metadata recovery overwrote later desired fields")
			}
			var reset model.ClientPolicyReset
			if err := db.First(&reset, "client_id = ?", f.client.StableID).Error; err != nil {
				t.Fatal(err)
			}
			reset.Id = 0
			if reset != f.snapshot.Resets[0] {
				t.Fatal("metadata recovery changed original semantic window")
			}
		})
	}
}

func TestManagedAuthorityRenewalExpiryOnlyColdRecoveryPreservesCount(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	past := setManagedRenewalDue(t, client, 3, 2)
	var before model.ClientRecord
	if err := database.GetDB().First(&before, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	if err := renewLocalClientPolicies(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	cfg := owner.config
	if err := stopManagedProcess(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	if err := db.Table("clients").Where("stable_id = ?", client.StableID).Updates(map[string]any{"expiry_time": past, "updated_at": before.UpdatedAt, "desired_policy_version": before.DesiredPolicyVersion, "policy_fingerprint": before.PolicyFingerprint}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Updates(map[string]any{"expiry_time": past, "reset_count": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}
	row := trafficOf(t, client.Email)
	if row.ExpiryTime != past+2*86400000 || row.ResetCount != 2 {
		t.Fatalf("expired recovery consumed another cap: %+v", row)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expiry-only recovery opened window: %d/%v", count, err)
	}
}
