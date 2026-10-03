package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

func TestManagedAuthorityRenewalSQLCommitFailureRecoversExactPreparation(t *testing.T) {
	testAuthorityRenewalCommitRecovery(t, true)
}

type authorityRenewalRecoveryFixture struct {
	svc      *XrayService
	inbound  *model.Inbound
	client   model.ClientRecord
	before   model.ClientRecord
	config   conf.ClientPolicyConfig
	capture  policyauthority.ResetOperationCapture
	prepared policyauthority.ResetOperationPreparation
	snapshot authorityRenewalPreparationSnapshot
	account  policyauthority.Account
}

func setupAuthorityRenewalRecoveryFixture(t *testing.T, laterReset bool) authorityRenewalRecoveryFixture {
	mode := "none"
	if laterReset {
		mode = "acknowledged"
	}
	return setupAuthorityRenewalRecoveryWithLaterBoundary(t, mode)
}

func setupAuthorityRenewalRecoveryWithLaterBoundary(t *testing.T, mode string) authorityRenewalRecoveryFixture {
	t.Helper()
	svc, inbound, client, _ := setupManagedActivationService(t)
	const callback = "test:renewal-later-reset-commit"
	var armed atomic.Bool
	if mode == "pending" {
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
		}
		if err := db.Exec("CREATE TABLE renewal_later_parent (id bigint PRIMARY KEY)").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("CREATE TABLE renewal_later_child (id bigint PRIMARY KEY, parent_id bigint REFERENCES renewal_later_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
			if armed.Load() && tx.Statement.Table == "client_policy_resets" {
				tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("INSERT INTO renewal_later_child (id,parent_id) VALUES (1,999999)").Error)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			armed.Store(false)
			_ = db.Callback().Create().Remove(callback)
			_ = db.Exec("DROP TABLE IF EXISTS renewal_later_child").Error
			_ = db.Exec("DROP TABLE IF EXISTS renewal_later_parent").Error
		})
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
	setManagedRenewalDue(t, client, 0, 2)
	f := authorityRenewalRecoveryFixture{svc: svc, inbound: inbound, client: *client, config: owner.config}
	if err := database.GetDB().First(&f.before, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := renewLocalClientPolicies(ctx, process); err != nil {
		t.Fatal(err)
	}
	page, err := owner.state.Journal.ResetOperationPage("", 128)
	if err != nil || len(page) != 1 {
		t.Fatalf("renewal fixture lacks capture: %d/%v", len(page), err)
	}
	f.capture, err = owner.state.Journal.LookupResetOperation(page[0].RequestID)
	if err != nil {
		t.Fatal(err)
	}
	f.prepared, err = owner.state.Journal.LookupResetPreparation(f.capture.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot, err = decodeAuthorityRenewalPreparation(f.prepared, f.capture, owner.state.Journal, owner.state.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "next")
	if mode != "none" {
		if mode == "pending" {
			armed.Store(true)
		}
		err := ResetLocalClientPolicy(ctx, client.StableID, "renewal-later-manual-reset")
		if mode == "pending" {
			armed.Store(false)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") || process.IsRunning() {
				t.Fatalf("later boundary did not fail at actual commit: %v", err)
			}
			if err := database.GetDB().Callback().Create().Remove(callback); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if process.IsRunning() {
		if err := stopManagedProcess(ctx, process); err != nil {
			t.Fatal(err)
		}
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	f.account, err = state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestManagedAuthorityRenewalRecoveryProjectsBusinessBeforePendingReset(t *testing.T) {
	f := setupAuthorityRenewalRecoveryWithLaterBoundary(t, "pending")
	restoreAuthorityRenewalSQL(t, f)
	if err := recoverAuthorityDesiredState(context.Background(), &f.config); err != nil {
		t.Fatal(err)
	}
	var client model.ClientRecord
	if err := database.GetDB().First(&client, "stable_id = ?", f.client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	if client.ExpiryTime != f.snapshot.Effects[0].AfterExpiryTime || trafficOf(t, f.client.Email).ResetCount != 1 {
		t.Fatalf("pending later reset obscured earlier renewal business effects: expiry=%d want=%d count=%d", client.ExpiryTime, f.snapshot.Effects[0].AfterExpiryTime, trafficOf(t, f.client.Email).ResetCount)
	}
	if err := f.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", f.inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "stay")
	if err := ResetLocalClientPolicy(context.Background(), f.client.StableID, "renewal-later-manual-reset"); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	if err := owner.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	account, err := owner.state.Journal.Account(f.client.StableID)
	if err != nil || account.Usage.BilledBytes != 348 || account.WindowBaseline != 332 || account.WindowUsed != 16 {
		t.Fatalf("pending later reset retry recomputed window: %+v/%v", account, err)
	}
}

func restoreAuthorityRenewalSQL(t *testing.T, f authorityRenewalRecoveryFixture) {
	t.Helper()
	db := database.GetDB()
	if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Updates(map[string]any{
		"expiry_time": f.before.ExpiryTime, "updated_at": f.before.UpdatedAt, "desired_policy_version": f.before.DesiredPolicyVersion, "policy_fingerprint": f.before.PolicyFingerprint,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", f.client.Email).Updates(map[string]any{"expiry_time": f.before.ExpiryTime, "reset_count": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&model.ClientPolicyReset{}, "client_id = ?", f.client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	var restored model.ClientRecord
	if err := db.First(&restored, "stable_id = ?", f.client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	if restored.ExpiryTime != f.before.ExpiryTime || restored.DesiredPolicyVersion != f.before.DesiredPolicyVersion || restored.PolicyFingerprint != f.before.PolicyFingerprint || restored.UpdatedAt != f.before.UpdatedAt || trafficOf(t, f.client.Email).ResetCount != 0 {
		t.Fatal("SQL restoration fixture did not restore the complete original business tuple")
	}
}

func TestManagedAuthorityRenewalRestoredSQLRetainsOriginalAndLaterWindows(t *testing.T) {
	for _, laterReset := range []bool{false, true} {
		t.Run(fmt.Sprintf("later-manual-reset-%t", laterReset), func(t *testing.T) {
			f := setupAuthorityRenewalRecoveryFixture(t, laterReset)
			restoreAuthorityRenewalSQL(t, f)
			if err := f.svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", f.inbound.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			managedActivationEcho(t, flow, "stay")
			if _, _, err := f.svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			owner := managedAuthorityForProcess(currentXrayProcess())
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			account, err := owner.state.Journal.Account(f.client.StableID)
			baseline := uint64(316)
			if laterReset {
				baseline = 332
			}
			if err != nil || account.Usage.BilledBytes != 348 || account.WindowBaseline != baseline || account.WindowUsed != 348-baseline {
				t.Fatalf("restored SQL changed original/later actual window: %+v/%v", account, err)
			}
			var client model.ClientRecord
			if err := database.GetDB().First(&client, "stable_id = ?", f.client.StableID).Error; err != nil {
				t.Fatal(err)
			}
			row := trafficOf(t, f.client.Email)
			if client.ExpiryTime != f.snapshot.Effects[0].AfterExpiryTime || row.ResetCount != 1 || row.ExpiryTime != client.ExpiryTime {
				t.Fatalf("restored SQL spent another renewal: %d/%+v", client.ExpiryTime, row)
			}
			prepared, err := owner.state.Journal.LookupResetPreparation(f.capture.RequestID)
			if err != nil || prepared != f.prepared {
				t.Fatalf("restored SQL recomputed preparation: %v", err)
			}
			page, err := owner.state.Journal.ResetOperationPage("", 128)
			want := 1
			if laterReset {
				want = 2
			}
			if err != nil || len(page) != want {
				t.Fatalf("restored SQL created another operation: %d/%v", len(page), err)
			}
		})
	}
}

func TestManagedAuthorityRenewalSQLCommitFailureRetainsLiveWindow(t *testing.T) {
	testAuthorityRenewalCommitRecovery(t, false)
}

func testAuthorityRenewalCommitRecovery(t *testing.T, fund bool) {
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
	}
	const callback = "test:renewal-deferred-commit"
	installDeferredCommitFailure(t, db, "create", callback, "client_policy_resets", "renewal_commit_parent", "renewal_commit_child")
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
	past := setManagedRenewalDue(t, client, 0, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = renewLocalClientPolicies(ctx, process)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") || process.IsRunning() {
		t.Fatalf("not an actual deferred renewal SQL commit failure/stopped core: %v/%t", err, process.IsRunning())
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	row := trafficOf(t, client.Email)
	var clientBefore model.ClientRecord
	if err := db.First(&clientBefore, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	if clientBefore.ExpiryTime != past || row.ResetCount != 0 {
		t.Fatalf("failed renewal SQL committed effects: %d/%d", clientBefore.ExpiryTime, row.ResetCount)
	}
	cfg := owner.config
	state, err := openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := state.Journal.ResetOperationPage("", 128)
	if err != nil || len(page) != 1 {
		t.Fatalf("failed commit lost renewal capture: %d/%v", len(page), err)
	}
	capture, err := state.Journal.LookupResetOperation(page[0].RequestID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := state.Journal.LookupResetPreparation(capture.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := decodeAuthorityRenewalPreparation(prepared, capture, state.Journal, state.SourceID)
	if err != nil || len(snapshot.Effects) != 1 || len(snapshot.Resets) != 1 || snapshot.Resets[0].BilledBytes != 316 {
		t.Fatalf("failed commit lost exact original renewal boundary: %+v/%v", snapshot, err)
	}
	if _, err := state.Journal.LookupResetCompletion(capture.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("failed commit acquired renewal completion: %v", err)
	}
	var grant policyauthority.Grant
	if fund {
		account, err := state.Journal.Account(client.StableID)
		if err != nil {
			t.Fatal(err)
		}
		boot := policyauthority.NodeBoot{NodeID: "local", SourceID: cfg.InstanceID, BootID: "renewal-cold-held-boot"}
		if err := state.Journal.RegisterBoot(boot); err != nil {
			t.Fatal(err)
		}
		grant, err = state.Journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: state.Journal.Identity(), NodeBoot: boot, ClientID: client.StableID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "renewal-cold-held40", ChallengeID: "renewal-cold-held-challenge", Capacity: 40, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
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
	var restored model.ClientRecord
	if err := db.First(&restored, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	row = trafficOf(t, client.Email)
	if restored.ExpiryTime != snapshot.Effects[0].AfterExpiryTime || row.ExpiryTime != restored.ExpiryTime || row.ResetCount != snapshot.Effects[0].AfterResetCount {
		t.Fatalf("cold failed-commit recovery omitted protected renewal effects: expiry=%d traffic-expiry=%d count=%d want=%d/%d", restored.ExpiryTime, row.ExpiryTime, row.ResetCount, snapshot.Effects[0].AfterExpiryTime, snapshot.Effects[0].AfterResetCount)
	}
	var reset model.ClientPolicyReset
	if err := db.First(&reset, "client_id = ? AND request_id = ?", client.StableID, snapshot.Resets[0].RequestID).Error; err != nil {
		t.Fatal(err)
	}
	reset.Id = 0
	if reset != snapshot.Resets[0] {
		t.Fatalf("recovery recomputed renewal reset: %+v/%+v", reset, snapshot.Resets[0])
	}
	state, err = openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := state.Journal.Account(client.StableID)
	if err != nil || before != after {
		t.Fatalf("renewal metadata recovery changed complete funded account: %v", err)
	}
	if fund {
		retained, err := state.Journal.Grant(grant.GrantID)
		if err != nil || retained != grant {
			t.Fatalf("renewal metadata recovery changed finite40 grant: %v", err)
		}
	}
	retained, err := state.Journal.LookupResetPreparation(capture.RequestID)
	if err != nil || retained != prepared {
		t.Fatalf("metadata recovery changed exact preparation: %v", err)
	}
	if _, err := state.Journal.LookupResetCompletion(capture.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("metadata recovery completed unexecuted renewal: %v", err)
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
	if err := renewLocalClientPolicies(ctx, currentXrayProcess()); err != nil {
		t.Fatal(err)
	}
	owner = managedAuthorityForProcess(currentXrayProcess())
	if _, err := owner.state.Journal.LookupResetCompletion(capture.RequestID); err != nil {
		t.Fatalf("actual core retry omitted renewal completion: %v", err)
	}
	managedActivationEcho(t, later, "stay")
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	after, err = owner.state.Journal.Account(client.StableID)
	if err != nil || after.Usage.BilledBytes != 348 || after.WindowBaseline != 316 || after.WindowUsed != 32 || trafficOf(t, client.Email).ResetCount != 1 {
		t.Fatalf("renewal retry lost lifetime/original allowance/window: %+v/%v", after, err)
	}
}
