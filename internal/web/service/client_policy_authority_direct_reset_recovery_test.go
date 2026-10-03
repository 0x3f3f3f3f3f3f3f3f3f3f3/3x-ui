package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

func TestManagedAuthorityDirectResetSQLCommitFailureRecoversExactPreparation(t *testing.T) {
	testAuthorityDirectResetCommitRecovery(t, true)
}

func TestManagedAuthorityDirectResetSQLCommitFailureRetainsLiveWindow(t *testing.T) {
	testAuthorityDirectResetCommitRecovery(t, false)
}

func testAuthorityDirectResetCommitRecovery(t *testing.T, fund bool) {
	t.Helper()
	svc, inbound, client, _ := setupManagedActivationService(t)
	db := database.GetDB()
	if db.Dialector.Name() == "sqlite" {
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			t.Fatal(err)
		}
		var enabled int
		if err := db.Raw("PRAGMA foreign_keys").Scan(&enabled).Error; err != nil || enabled != 1 {
			t.Fatalf("actual deferred commit enforcement absent: %d/%v", enabled, err)
		}
	}
	const callback = "test:direct-reset-deferred-commit"
	installDeferredCommitFailure(t, db, "create", callback, "client_policy_resets", "direct_commit_parent", "direct_commit_child")
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	cfg := owner.config
	const request = "direct-prepared-before-sql-commit"
	err = ResetLocalClientPolicy(ctx, client.StableID, request)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") || process.IsRunning() {
		t.Fatalf("not an actual deferred SQL commit failure/stopped core: %v/%t", err, process.IsRunning())
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed transaction retained semantic row: %d/%v", count, err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	key := authorityDirectResetKey(request, []string{client.StableID})
	capture, err := state.Journal.LookupResetOperation(key)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := state.Journal.LookupResetPreparation(key)
	if err != nil {
		t.Fatalf("direct SQL failure lost prepared boundary: %v", err)
	}
	snapshot, err := decodeAuthorityDirectResetPreparation(prepared, capture, state.Journal, state.SourceID)
	if err != nil || len(snapshot.Resets) != 1 || snapshot.Resets[0].BilledBytes != 316 {
		t.Fatalf("direct failed commit prepared wrong boundary: %+v/%v", snapshot, err)
	}
	if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("direct failed SQL commit acquired completion: %v", err)
	}
	var grant policyauthority.Grant
	if fund {
		account, err := state.Journal.Account(client.StableID)
		if err != nil {
			t.Fatal(err)
		}
		boot := policyauthority.NodeBoot{NodeID: "local", SourceID: cfg.InstanceID, BootID: "direct-cold-held-boot"}
		if err := state.Journal.RegisterBoot(boot); err != nil {
			t.Fatal(err)
		}
		grant, err = state.Journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: state.Journal.Identity(), NodeBoot: boot, ClientID: client.StableID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "direct-cold-held40", ChallengeID: "direct-cold-held-challenge", Capacity: 40, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	var restored model.ClientPolicyReset
	if err := db.First(&restored, "client_id = ? AND request_id = ?", client.StableID, request).Error; err != nil {
		t.Fatalf("cold recovery lost direct prepared semantic boundary: %v", err)
	}
	restored.Id = 0
	if restored != snapshot.Resets[0] {
		t.Fatalf("cold recovery recomputed direct boundary: %+v %+v", restored, snapshot.Resets[0])
	}
	var stamp model.ClientTrafficResetTime
	if err := db.First(&stamp, "client_id = ?", client.StableID).Error; err != nil || stamp.EffectiveAt != restored.CreatedAt {
		t.Fatalf("cold recovery lost exact direct effect time: %+v/%v", stamp, err)
	}
	state, err = openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := state.Journal.Account(client.StableID)
	if err != nil || before != after {
		t.Fatalf("direct metadata recovery changed complete account/held liability: %v", err)
	}
	if fund {
		retained, err := state.Journal.Grant(grant.GrantID)
		if err != nil || retained != grant {
			t.Fatalf("direct metadata recovery changed finite40 grant: %v", err)
		}
	}
	retainedCapture, err := state.Journal.LookupResetOperation(key)
	if err != nil || retainedCapture != capture {
		t.Fatalf("direct metadata recovery changed capture: %v", err)
	}
	retainedPreparation, err := state.Journal.LookupResetPreparation(key)
	if err != nil || retainedPreparation != prepared {
		t.Fatalf("direct metadata recovery changed preparation: %v", err)
	}
	if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("direct metadata recovery claimed execution: %v", err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if fund {
		return
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	later, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer later.Close()
	managedActivationEcho(t, later, "next")
	if err := ResetLocalClientPolicy(ctx, client.StableID, request); err != nil {
		t.Fatal(err)
	}
	owner = managedAuthorityForProcess(currentXrayProcess())
	retainedPreparation, err = owner.state.Journal.LookupResetPreparation(key)
	if err != nil || retainedPreparation != prepared {
		t.Fatalf("actual core retry changed interrupted preparation: %v", err)
	}
	if _, err := owner.state.Journal.LookupResetCompletion(key); err != nil {
		t.Fatalf("actual core retry omitted direct completion: %v", err)
	}
	managedActivationEcho(t, later, "stay")
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	after, err = owner.state.Journal.Account(client.StableID)
	if err != nil || after.WindowBaseline != 316 || after.Usage.BilledBytes != 348 || after.WindowUsed != 32 {
		t.Fatalf("interrupted direct retry lost actual2x lifetime/window: %+v/%v", after, err)
	}
}

func setupAuthorityDirectRecoveryFixture(t *testing.T, laterReset bool) authorityExecutionRecoveryFixture {
	t.Helper()
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	var f authorityExecutionRecoveryFixture
	f.client = *client
	if err := db.First(&f.seedProjection, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const request = "direct-cold-original"
	if err := ResetLocalClientPolicy(ctx, client.StableID, request); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&f.originalReset, "client_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "next")
	if laterReset {
		if err := ResetLocalClientPolicy(ctx, client.StableID, "direct-newer-acknowledged-reset"); err != nil {
			t.Fatal(err)
		}
		managedActivationEcho(t, flow, "more")
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	f.config = owner.config
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	key := authorityDirectResetKey(request, []string{client.StableID})
	f.capture, err = state.Journal.LookupResetOperation(key)
	if err != nil {
		t.Fatal(err)
	}
	f.prepared, err = state.Journal.LookupResetPreparation(key)
	if err != nil {
		t.Fatal(err)
	}
	f.completed, err = state.Journal.LookupResetCompletion(key)
	if err != nil {
		t.Fatal(err)
	}
	account, err := state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	boot := policyauthority.NodeBoot{NodeID: "local", SourceID: f.config.InstanceID, BootID: "direct-cold-held-boot"}
	if err := state.Journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	f.grant, err = state.Journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: state.Journal.Identity(), NodeBoot: boot, ClientID: client.StableID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "direct-cold-held40", ChallengeID: "direct-cold-held-challenge", Capacity: 40, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.account, err = state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestManagedAuthorityDirectResetColdRecoveryPreservesIdentityAndLaterWindow(t *testing.T) {
	for _, kind := range []string{"original", "rename-email-reuse", "delete-email-reuse", "surrogate-id-collision", "later-reset"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAuthorityDirectRecoveryFixture(t, kind == "later-reset")
			db := database.GetDB()
			if err := db.Where("1 = 1").Delete(&model.ClientPolicyReset{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("client_id = ?", f.client.StableID).Delete(&model.ClientTrafficResetTime{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Updates(map[string]any{"desired_policy_version": f.seedProjection.DesiredPolicyVersion, "policy_fingerprint": f.seedProjection.PolicyFingerprint}).Error; err != nil {
				t.Fatal(err)
			}
			if kind == "rename-email-reuse" {
				if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Update("email", "direct-renamed-original").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", f.client.Email).Update("email", "direct-renamed-original").Error; err != nil {
					t.Fatal(err)
				}
			}
			if kind == "delete-email-reuse" {
				if err := db.Where("stable_id = ?", f.client.StableID).Delete(&model.ClientRecord{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Where("email = ?", f.client.Email).Delete(&xray.ClientTraffic{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if kind == "rename-email-reuse" || kind == "delete-email-reuse" {
				replacement := model.ClientRecord{Email: f.client.Email, Enable: true}
				if err := db.Create(&replacement).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&xray.ClientTraffic{Email: replacement.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if kind == "surrogate-id-collision" {
				occupied := f.originalReset
				occupied.ClientID, occupied.RequestID = uuid.NewString(), "direct-occupied-unrelated"
				if err := db.Create(&occupied).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverAuthorityDesiredState(context.Background(), &f.config); err != nil {
				t.Fatal(err)
			}
			assertAuthorityExecutionRecoveryUnchanged(t, f)
			var restored model.ClientPolicyReset
			if err := db.First(&restored, "client_id = ? AND request_id = ?", f.client.StableID, f.originalReset.RequestID).Error; err != nil {
				t.Fatal(err)
			}
			if kind == "surrogate-id-collision" && restored.Id == f.originalReset.Id {
				t.Fatal("direct recovery reused unrelated occupied numeric identity")
			}
			restored.Id = f.originalReset.Id
			if restored != f.originalReset {
				t.Fatalf("direct recovery lost original semantic identity: %+v %+v", restored, f.originalReset)
			}
			if kind == "delete-email-reuse" {
				var count int64
				if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", f.client.StableID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("direct recovery recreated deleted UUID: %d/%v", count, err)
				}
			} else {
				latest, err := latestClientPolicyResets(db, []string{f.client.StableID})
				if err != nil {
					t.Fatal(err)
				}
				want := f.originalReset.RequestID
				if kind == "later-reset" {
					want = "direct-newer-acknowledged-reset"
				}
				if latest[f.client.StableID] == nil || latest[f.client.StableID].RequestID != want {
					t.Fatalf("direct recovery superseded newer acknowledged window: %+v", latest)
				}
			}
			if kind == "rename-email-reuse" || kind == "delete-email-reuse" {
				var traffic xray.ClientTraffic
				if err := db.First(&traffic, "email = ?", f.client.Email).Error; err != nil || traffic.Up != 7 || traffic.Down != 11 {
					t.Fatalf("direct recovery charged reused email: %+v/%v", traffic, err)
				}
			}
		})
	}
}

func TestManagedAuthorityDirectResetColdRecoveryPreservesLaterDesiredMultiplier(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	var seed model.ClientRecord
	if err := db.First(&seed, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const request = "direct-before-later-desired-edit"
	if err := ResetLocalClientPolicy(ctx, client.StableID, request); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	if err := db.Table("clients").Where("stable_id = ?", client.StableID).Updates(map[string]any{"desired_policy_version": seed.DesiredPolicyVersion, "policy_fingerprint": seed.PolicyFingerprint, "policy_multiplier": "0.5"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&model.ClientPolicyReset{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("direct prepared history prevented later desired edit: %v", err)
	}
	var cfg conf.ClientPolicyConfig
	if err := json.Unmarshal(currentXrayProcess().GetConfig().ClientPolicy, &cfg); err != nil || len(cfg.Policies) != 1 || cfg.Policies[0].Version != 3 || cfg.Policies[0].Multiplier != 500000 {
		t.Fatalf("actual core lost later desired0.5 multiplier/version3: %+v/%v", cfg.Policies, err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	key := authorityDirectResetKey(request, []string{client.StableID})
	prepared, err := owner.state.Journal.LookupResetPreparation(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := ResetLocalClientPolicy(ctx, client.StableID, request); err != nil {
		t.Fatal(err)
	}
	retained, err := owner.state.Journal.LookupResetPreparation(key)
	if err != nil || retained != prepared {
		t.Fatalf("later desired edit changed original direct preparation: %v", err)
	}
}

func TestManagedAuthorityDirectResetColdRecoveryRefusesInvalidEvidence(t *testing.T) {
	for _, kind := range []string{"schema", "unknown-field", "request", "usage-ahead", "duplicate-row", "foreign-source", "cancelled", "foreign-handle"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAuthorityDirectRecoveryFixture(t, false)
			ctx := context.Background()
			want := ErrClientPolicyLedger
			if kind == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
				want = context.Canceled
			} else if kind == "foreign-handle" {
				state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
				if err != nil {
					t.Fatal(err)
				}
				err = recoverAuthorityResetCaptures(ctx, database.GetDB().Session(&gorm.Session{NewDB: true}), state.Journal, state.SourceID)
				_ = state.Journal.Close()
				if !errors.Is(err, ErrDatabaseReplaced) {
					t.Fatalf("direct recovery admitted foreign handle: %v", err)
				}
				assertAuthorityExecutionRecoveryUnchanged(t, f)
				return
			} else {
				state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
				if err != nil {
					t.Fatal(err)
				}
				request := "direct-invalid-" + kind
				ids := []string{f.client.StableID}
				key := authorityDirectResetKey(request, ids)
				raw, _ := json.Marshal(authorityDirectResetCaptureSnapshot{Schema: 1, RequestID: request, ClientIDs: ids})
				capture := policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: state.SourceID, RequestID: key, Snapshot: string(raw)}
				if err := state.Journal.CaptureResetOperation(capture); err != nil {
					t.Fatal(err)
				}
				reset := f.originalReset
				reset.Id, reset.RequestID = 0, request
				snapshot := authorityDirectResetPreparationSnapshot{Schema: 1, RequestID: request, Resets: []model.ClientPolicyReset{reset}}
				switch kind {
				case "schema":
					snapshot.Schema = 99
				case "request":
					snapshot.RequestID = "wrong-direct-request"
				case "usage-ahead":
					snapshot.Resets[0].RawUpload += 1000
				case "duplicate-row":
					snapshot.Resets = append(snapshot.Resets, reset)
				case "foreign-source":
					snapshot.Resets[0].InstanceID = "foreign-direct-source"
				}
				raw, _ = json.Marshal(snapshot)
				if kind == "unknown-field" {
					raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
				}
				prepared := policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: key, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: string(raw)}
				if err := state.Journal.PrepareResetOperation(prepared); err != nil {
					t.Fatalf("business-invalid fixture was not a real durable preparation: %v", err)
				}
				if err := state.Journal.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverAuthorityDesiredState(ctx, &f.config); !errors.Is(err, want) {
				t.Fatalf("invalid direct %s admitted: %v", kind, err)
			}
			assertAuthorityExecutionRecoveryUnchanged(t, f)
		})
	}
}
