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
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/infra/conf"
)

func TestManagedAuthorityResetExecutionSQLCommitFailureRecoversExactPreparation(t *testing.T) {
	testManagedAuthorityPreparedSQLCommitRecovery(t, true)
}

func TestManagedAuthorityResetExecutionSQLCommitFailureRetainsLiveBoundary(t *testing.T) {
	testManagedAuthorityPreparedSQLCommitRecovery(t, false)
}

func testManagedAuthorityPreparedSQLCommitRecovery(t *testing.T, fund bool) {
	t.Helper()
	svc, tunnel, client, _ := setupManagedActivationService(t)
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
			t.Fatalf("FK enforcement absent: %d/%v", enabled, err)
		}
	}
	const callback = "test:managed-preparation-deferred-commit"
	installDeferredCommitFailure(t, db, "create", callback, "client_policy_resets", "prepared_commit_parent", "prepared_commit_child")
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	process := currentXrayProcess()
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		t.Fatal(err)
	}
	const request = "execution-prepared-before-sql-commit"
	_, resetErr := (&ClientService{}).ResetAllTrafficsWithRequest(ctx, request)
	if resetErr == nil || !strings.Contains(strings.ToLower(resetErr.Error()), "foreign key") {
		t.Fatalf("did not fail at actual deferred SQL commit: %v", resetErr)
	}
	if process.IsRunning() {
		t.Fatal("failed SQL preparation left core serving")
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Where("client_id = ?", client.StableID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed commit retained reset SQL rows: %d/%v", count, err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	key := authorityResetRequestKey(request)
	preparation, err := state.Journal.LookupResetPreparation(key)
	if err != nil {
		t.Fatalf("commit failure lost durable preparation: %v", err)
	}
	if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("SQL failure gained completion: %v", err)
	}
	account, err := state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	var grant policyauthority.Grant
	if fund {
		boot := policyauthority.NodeBoot{NodeID: "local", SourceID: config.InstanceID, BootID: "prepared-recovery-held-boot"}
		if err := state.Journal.RegisterBoot(boot); err != nil {
			t.Fatal(err)
		}
		grant, err = state.Journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: state.Journal.Identity(), NodeBoot: boot, ClientID: client.StableID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "prepared-recovery-held40", ChallengeID: "prepared-recovery-held-challenge", Capacity: 40, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
		if err != nil {
			t.Fatal(err)
		}
	}
	funded, err := state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(ctx, &config); err != nil {
		t.Fatal(err)
	}
	var restored model.ClientPolicyReset
	if err := db.First(&restored, "client_id = ?", client.StableID).Error; err != nil {
		t.Fatalf("cold recovery lost original prepared reset after failed SQL commit: %v", err)
	}
	if restored.RawUpload != 104 || restored.RawDownload != 204 || restored.BilledBytes != 316 || restored.PolicyVersion != 2 {
		t.Fatalf("cold recovery recomputed original prepared boundary: %+v", restored)
	}
	state, err = openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	retained, err := state.Journal.LookupResetPreparation(key)
	if err != nil || retained != preparation {
		t.Fatalf("cold recovery changed original preparation: %v", err)
	}
	after, err := state.Journal.Account(client.StableID)
	if err != nil || after != funded {
		t.Fatalf("cold preparation projection spent/released held grant: %v", err)
	}
	if fund {
		held, err := state.Journal.Grant(grant.GrantID)
		if err != nil || held != grant {
			t.Fatalf("cold preparation projection changed finite40 grant: %v", err)
		}
	}
	if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("cold metadata recovery falsely acknowledged core: %v", err)
	}
	if fund {
		return
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	live, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	managedActivationEcho(t, live, "next")
	owner := managedAuthorityForProcess(currentXrayProcess())
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	beforeRetry, err := owner.state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetAllTrafficsWithRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	afterRetry, err := owner.state.Journal.Account(client.StableID)
	if err != nil || beforeRetry.Policy != afterRetry.Policy || beforeRetry.WindowBaseline != afterRetry.WindowBaseline || afterRetry.WindowBaseline != 316 || afterRetry.WindowUsed != 16 {
		t.Fatalf("original prepared retry moved baseline: %+v/%v", afterRetry, err)
	}
	if _, err := owner.state.Journal.LookupResetCompletion(key); err != nil {
		t.Fatalf("successful retry lacks actual core acknowledgement: %v", err)
	}
	retained, err = owner.state.Journal.LookupResetPreparation(key)
	if err != nil || retained != preparation {
		t.Fatalf("successful retry changed original preparation: %v", err)
	}
	managedActivationEcho(t, live, "stay")
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	afterRetry, err = owner.state.Journal.Account(client.StableID)
	if err != nil || afterRetry.Usage.BilledBytes != 348 || afterRetry.WindowBaseline != 316 || afterRetry.WindowUsed != 32 {
		t.Fatalf("live exact2x after SQL interruption lost: %+v/%v", afterRetry, err)
	}
}

func TestManagedAuthorityResetExecutionColdRecoveryPreservesLaterDesiredEdit(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := database.GetDB()
	var before model.ClientRecord
	if err := db.First(&before, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	const request = "execution-before-later-desired-edit"
	if _, err := (&ClientService{}).ResetAllTrafficsWithRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	// Restore disposable version/fingerprint projection while keeping the user's
	// newer desired multiplier. It must become a later monotone policy version.
	if err := db.Table("clients").Where("stable_id = ?", client.StableID).Updates(map[string]any{"desired_policy_version": before.DesiredPolicyVersion, "policy_fingerprint": before.PolicyFingerprint, "policy_multiplier": "0.5"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&model.ClientPolicyReset{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("request_id = ?", request).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("original prepared recovery prevented newer desired policy activation: %v", err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	var saved model.ClientRecord
	if err := db.First(&saved, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Policy == nil || saved.Policy.Multiplier != "0.5" || saved.DesiredPolicyVersion != 3 {
		t.Fatalf("recovery rewound newer desired config/version: %+v", saved)
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Policies) != 1 || config.Policies[0].Version != 3 || config.Policies[0].Multiplier != 500000 {
		t.Fatalf("newer desired policy missing from actual core config: %+v", config.Policies)
	}
	preparation, err := owner.state.Journal.LookupResetPreparation(authorityResetRequestKey(request))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetAllTrafficsWithRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	retained, err := owner.state.Journal.LookupResetPreparation(preparation.RequestID)
	if err != nil || retained != preparation {
		t.Fatalf("later desired edit rewrote original preparation: %v", err)
	}
}

type authorityExecutionRecoveryFixture struct {
	config         conf.ClientPolicyConfig
	client         model.ClientRecord
	seedProjection model.ClientRecord
	originalReset  model.ClientPolicyReset
	capture        policyauthority.ResetOperationCapture
	prepared       policyauthority.ResetOperationPreparation
	completed      policyauthority.ResetOperationCompletion
	account        policyauthority.Account
	grant          policyauthority.Grant
}

func setupAuthorityExecutionRecoveryFixture(t *testing.T, laterReset bool) authorityExecutionRecoveryFixture {
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
	const request = "execution-cold-original"
	if _, err := (&ClientService{}).ResetAllTrafficsWithRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&f.originalReset, "client_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "next")
	if laterReset {
		if err := ResetLocalClientPolicy(ctx, client.StableID, "execution-newer-acknowledged-reset"); err != nil {
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
	key := authorityResetRequestKey(request)
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
	boot := policyauthority.NodeBoot{NodeID: "local", SourceID: f.config.InstanceID, BootID: "execution-cold-held-boot"}
	if err := state.Journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	f.grant, err = state.Journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: state.Journal.Identity(), NodeBoot: boot, ClientID: client.StableID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "execution-cold-held40", ChallengeID: "execution-cold-held-challenge", Capacity: 40, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.account, err = state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func assertAuthorityExecutionRecoveryUnchanged(t *testing.T, f authorityExecutionRecoveryFixture) {
	t.Helper()
	state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	account, err := state.Journal.Account(f.client.StableID)
	if err != nil || account != f.account {
		t.Fatalf("metadata recovery changed full funded account: %v", err)
	}
	grant, err := state.Journal.Grant(f.grant.GrantID)
	if err != nil || grant != f.grant {
		t.Fatalf("metadata recovery changed finite40 grant: %v", err)
	}
	capture, err := state.Journal.LookupResetOperation(f.capture.RequestID)
	if err != nil || capture != f.capture {
		t.Fatalf("metadata recovery changed immutable capture: %v", err)
	}
	prepared, err := state.Journal.LookupResetPreparation(f.prepared.RequestID)
	if err != nil || prepared != f.prepared {
		t.Fatalf("metadata recovery changed immutable preparation: %v", err)
	}
	completed, err := state.Journal.LookupResetCompletion(f.completed.RequestID)
	if err != nil || completed != f.completed {
		t.Fatalf("metadata recovery changed original completion: %v", err)
	}
}

func TestManagedAuthorityResetExecutionColdRecoveryPreservesIdentityAndLaterWindow(t *testing.T) {
	for _, kind := range []string{"original", "renamed-email-reuse", "deleted-email-reuse", "surrogate-id-collision", "later-acknowledged-reset"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAuthorityExecutionRecoveryFixture(t, kind == "later-acknowledged-reset")
			db := database.GetDB()
			if err := db.Where("1 = 1").Delete(&model.ClientPolicyReset{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("request_id = ?", "execution-cold-original").Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("client_id = ?", f.client.StableID).Delete(&model.ClientTrafficResetTime{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Updates(map[string]any{"desired_policy_version": f.seedProjection.DesiredPolicyVersion, "policy_fingerprint": f.seedProjection.PolicyFingerprint}).Error; err != nil {
				t.Fatal(err)
			}
			if kind == "renamed-email-reuse" {
				if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Update("email", "execution-renamed-original").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", f.client.Email).Update("email", "execution-renamed-original").Error; err != nil {
					t.Fatal(err)
				}
			}
			if kind == "deleted-email-reuse" {
				if err := db.Where("stable_id = ?", f.client.StableID).Delete(&model.ClientRecord{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Where("email = ?", f.client.Email).Delete(&xray.ClientTraffic{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			var replacement model.ClientRecord
			if kind == "renamed-email-reuse" || kind == "deleted-email-reuse" {
				replacement = model.ClientRecord{Email: f.client.Email, Enable: true}
				if err := db.Create(&replacement).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&xray.ClientTraffic{Email: replacement.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if kind == "surrogate-id-collision" {
				occupied := f.originalReset
				occupied.ClientID = uuid.NewString()
				occupied.RequestID = "occupied-unrelated-surrogate"
				if err := db.Create(&occupied).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverAuthorityDesiredState(context.Background(), &f.config); err != nil {
				t.Fatal(err)
			}
			assertAuthorityExecutionRecoveryUnchanged(t, f)
			var operation model.ClientTrafficResetBatch
			if err := db.First(&operation, "request_id = ?", "execution-cold-original").Error; err != nil || !operation.Applied || operation.Affected != 1 {
				t.Fatalf("original operation result lost: %+v/%v", operation, err)
			}
			var reset model.ClientPolicyReset
			if err := db.First(&reset, "client_id = ? AND request_id = ?", f.client.StableID, f.originalReset.RequestID).Error; err != nil {
				t.Fatal(err)
			}
			if kind == "surrogate-id-collision" && reset.Id == f.originalReset.Id {
				t.Fatal("recovery reused occupied numeric reset identity")
			}
			reset.Id = f.originalReset.Id
			if reset != f.originalReset {
				t.Fatalf("semantic original reset changed: %+v", reset)
			}
			if kind == "deleted-email-reuse" {
				var count int64
				if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", f.client.StableID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("deleted UUID recreated: %d/%v", count, err)
				}
				if _, err := (&ClientService{}).ResetAllTrafficsWithRequest(context.Background(), "execution-cold-original"); err != nil {
					t.Fatal(err)
				}
				assertAuthorityExecutionRecoveryUnchanged(t, f)
			} else {
				var current model.ClientRecord
				if err := db.First(&current, "stable_id = ?", f.client.StableID).Error; err != nil {
					t.Fatal(err)
				}
				if current.DesiredPolicyVersion != int64(f.account.Policy.Version) {
					t.Fatalf("cold recovery rewound acknowledged version: %d/%d", current.DesiredPolicyVersion, f.account.Policy.Version)
				}
				latest, err := latestClientPolicyResets(db, []string{f.client.StableID})
				if err != nil {
					t.Fatal(err)
				}
				expected := f.originalReset.RequestID
				if kind == "later-acknowledged-reset" {
					expected = "execution-newer-acknowledged-reset"
				}
				if latest[f.client.StableID] == nil || latest[f.client.StableID].RequestID != expected {
					t.Fatalf("original preparation superseded newer reset: %+v", latest)
				}
			}
			if replacement.StableID != "" {
				var traffic xray.ClientTraffic
				if err := db.First(&traffic, "email = ?", replacement.Email).Error; err != nil || traffic.Up != 7 || traffic.Down != 11 {
					t.Fatalf("reused email rebound reset effects: %d/%d/%v", traffic.Up, traffic.Down, err)
				}
			}
		})
	}
}

func TestManagedAuthorityResetExecutionRecoveryRefusesInvalidProgress(t *testing.T) {
	for _, fault := range []string{"schema", "unknown-field", "request", "usage-ahead", "duplicate-id", "foreign-source", "cancelled", "replaced-pool", "immutable-sql"} {
		t.Run(fault, func(t *testing.T) {
			f := setupAuthorityExecutionRecoveryFixture(t, false)
			expected := database.GetDB()
			state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			ctx := context.Background()
			source := f.config.InstanceID
			if fault == "foreign-source" {
				source = "foreign-preparation-source"
			} else if fault == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			} else if fault == "replaced-pool" {
				dbtest.InitDB(t, filepath.Join(t.TempDir(), "execution-replacement.db"))
			} else if fault == "immutable-sql" {
				if err := expected.Table("client_traffic_reset_batches").Where("request_id = ?", "execution-cold-original").Update("affected", 99).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				original, err := decodeAuthorityResetCapture(f.capture, state.Journal, source)
				if err != nil {
					t.Fatal(err)
				}
				original.Operation.RequestID = "execution-invalid-progress"
				raw, err := json.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				capture := policyauthority.ResetOperationCapture{Identity: f.capture.Identity, SourceID: source, RequestID: authorityResetRequestKey(original.Operation.RequestID), Snapshot: string(raw)}
				if err := state.Journal.CaptureResetOperation(capture); err != nil {
					t.Fatal(err)
				}
				var prepared authorityResetPreparationSnapshot
				if err := json.Unmarshal([]byte(f.prepared.Snapshot), &prepared); err != nil {
					t.Fatal(err)
				}
				prepared.RequestID = original.Operation.RequestID
				prepared.Resets[0].RequestID = "batch:" + authorityResetSnapshotDigest(prepared.RequestID)
				switch fault {
				case "schema":
					prepared.Schema = 99
				case "request":
					prepared.RequestID = "wrong-semantic-request"
				case "usage-ahead":
					prepared.Resets[0].BilledBytes = int64(f.account.Usage.BilledBytes) + 1
				case "duplicate-id":
					prepared.ActiveManagedIDs = append(prepared.ActiveManagedIDs, prepared.ActiveManagedIDs[0])
					prepared.Resets = append(prepared.Resets, prepared.Resets[0])
					prepared.Affected++
				}
				raw, err = json.Marshal(prepared)
				if err != nil {
					t.Fatal(err)
				}
				if fault == "unknown-field" {
					raw = append(raw[:len(raw)-1], []byte(",\"Unknown\":true}")...)
				}
				if err := state.Journal.PrepareResetOperation(policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: source, RequestID: capture.RequestID, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: string(raw)}); err != nil {
					t.Fatal(err)
				}
			}
			want := ErrClientPolicyLedger
			if fault == "cancelled" {
				want = context.Canceled
			} else if fault == "replaced-pool" {
				want = database.ErrDatabaseReplaced
			}
			if err := recoverAuthorityResetCaptures(ctx, expected, state.Journal, source); !errors.Is(err, want) {
				t.Fatalf("unsafe prepared recovery admitted %s: %v", fault, err)
			}
			if err := state.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			assertAuthorityExecutionRecoveryUnchanged(t, f)
		})
	}
}

func TestManagedAuthorityResetExecutionColdRecoveryRetainsCalendarNoOp(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	db := database.GetDB()
	if err := db.Model(client).Update("traffic_reset", "daily").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	var seed model.ClientRecord
	if err := db.First(&seed, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ResetLocalClientPolicy(ctx, client.StableID, "execution-noop-prior-manual"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := (&ClientService{}).RunScheduledTrafficReset(ctx, "daily", now); err != nil {
		t.Fatal(err)
	}
	var original model.ClientTrafficResetBatch
	if err := db.First(&original, "scheduled_at > 0").Error; err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	config := owner.config
	prepared, err := owner.state.Journal.LookupResetPreparation(authorityResetRequestKey(original.RequestID))
	if err != nil {
		t.Fatal(err)
	}
	completed, err := owner.state.Journal.LookupResetCompletion(prepared.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("request_id = ?", original.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&model.ClientPolicyReset{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&model.ClientTrafficResetTime{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("clients").Where("stable_id = ?", client.StableID).Updates(map[string]any{"desired_policy_version": seed.DesiredPolicyVersion, "policy_fingerprint": seed.PolicyFingerprint, "traffic_reset": "monthly"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(ctx, &config); err != nil {
		t.Fatal(err)
	}
	var restored model.ClientTrafficResetBatch
	if err := db.First(&restored, "request_id = ?", original.RequestID).Error; err != nil || restored != original {
		t.Fatalf("cold recovery recomputed original no-op result: %+v/%v", restored, err)
	}
	// Eligibility/settings changed, but the original acknowledged empty action
	// remains empty and can retry without a running core or new reset boundary.
	if err := (&ClientService{}).RunScheduledTrafficReset(ctx, "daily", now); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	retained, err := state.Journal.LookupResetPreparation(prepared.RequestID)
	if err != nil || retained != prepared {
		t.Fatalf("no-op retry changed original preparation: %v", err)
	}
	done, err := state.Journal.LookupResetCompletion(completed.RequestID)
	if err != nil || done != completed {
		t.Fatalf("no-op retry changed original completion: %v", err)
	}
	var resets []model.ClientPolicyReset
	if err := db.Where("client_id = ?", client.StableID).Find(&resets).Error; err != nil || len(resets) != 1 || resets[0].RequestID != "execution-noop-prior-manual" {
		t.Fatalf("calendar no-op created a new reset: %+v/%v", resets, err)
	}
}
