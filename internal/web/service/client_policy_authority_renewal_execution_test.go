package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
)

func setManagedRenewalDue(t *testing.T, client *model.ClientRecord, days int, limit int) int64 {
	t.Helper()
	past := time.Now().Add(-time.Duration(days)*24*time.Hour - time.Hour).UnixMilli()
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", client.StableID).
		Updates(map[string]any{"expiry_time": past, "reset": 1, "reset_max": limit}).Error; err != nil {
		t.Fatal(err)
	}
	return past
}

func TestManagedAuthorityRenewalExecutionFailureRetainsPreparation(t *testing.T) {
	for _, kind := range []string{"core-application", "completion-record"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, client, _ := setupManagedActivationService(t)
			db := database.GetDB()
			injected := errors.New("renewal " + kind + " unavailable")
			var armed, reached atomic.Bool
			const callback = "test:renewal-completion"
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if armed.Load() && tx.Statement.Table == "client_policy_sources" {
					reached.Store(true)
					tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { armed.Store(false); _ = db.Callback().Query().Remove(callback) })
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			owner := managedAuthorityForProcess(process)
			setManagedRenewalDue(t, client, 0, 2)
			manager := panelruntime.GetManager()
			measured := &measuredResetRuntime{Runtime: manager.Local(), ManagedProcessRuntime: manager.Local().(panelruntime.ManagedProcessRuntime)}
			var observed error
			measured.beforeApply = func() {
				page, err := owner.state.Journal.ResetOperationPage("", 128)
				if err != nil || len(page) != 1 {
					observed = fmt.Errorf("actual core application without renewal capture: %d/%v", len(page), err)
					return
				}
				if _, err := owner.state.Journal.LookupResetPreparation(page[0].RequestID); err != nil {
					observed = err
				}
				if _, err := owner.state.Journal.LookupResetCompletion(page[0].RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
					observed = fmt.Errorf("completion preceded core reply: %v", err)
				}
				if kind == "completion-record" {
					armed.Store(true)
				}
			}
			if kind == "core-application" {
				measured.failApplyAt, measured.failure = 1, injected
			}
			manager.SetLocalRuntimeOverride(measured)
			err := renewLocalClientPolicies(context.Background(), process)
			manager.SetLocalRuntimeOverride(nil)
			armed.Store(false)
			if !errors.Is(err, injected) || observed != nil || process.IsRunning() || measured.applications != 1 || kind == "completion-record" && !reached.Load() {
				t.Fatalf("renewal failure boundary lost: %v observed=%v applies=%d reached=%t running=%t", err, observed, measured.applications, reached.Load(), process.IsRunning())
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(owner.config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			page, err := state.Journal.ResetOperationPage("", 128)
			if err != nil || len(page) != 1 {
				t.Fatalf("failed renewal lost durable capture: %d/%v", len(page), err)
			}
			if _, err := state.Journal.LookupResetPreparation(page[0].RequestID); err != nil {
				t.Fatal(err)
			}
			if _, err := state.Journal.LookupResetCompletion(page[0].RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("failed renewal was completed: %v", err)
			}
		})
	}
}

func TestManagedAuthorityRenewalRejectedAdmissionKeepsLiveCore(t *testing.T) {
	for _, kind := range []string{"queued-cancellation", "unknown-uuid", "lost-owner", "foreign-database", "foreign-source"} {
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
			before, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			setManagedRenewalDue(t, client, 0, 2)
			ids := []string{client.StableID}
			restore := func() {}
			want := ErrClientPolicyLedger
			switch kind {
			case "unknown-uuid":
				unknown := model.ClientRecord{Email: "unknown-renewal-owner", ExpiryTime: time.Now().Add(-time.Hour).UnixMilli(), Reset: 1, Enable: true}
				if err := database.GetDB().Create(&unknown).Error; err != nil {
					t.Fatal(err)
				}
				ids = append(ids, unknown.StableID)
				want = policyauthority.ErrNotFound
			case "lost-owner":
				localAuthority.Lock()
				localAuthority.process, localAuthority.authority = nil, nil
				localAuthority.Unlock()
				restore = func() {
					localAuthority.Lock()
					localAuthority.process, localAuthority.authority = process, owner
					localAuthority.Unlock()
				}
				want = ErrAuthorityNotInitialized
			case "foreign-database":
				db := owner.db
				owner.mu.Lock()
				owner.db = db.Session(&gorm.Session{NewDB: true})
				owner.mu.Unlock()
				restore = func() { owner.mu.Lock(); owner.db = db; owner.mu.Unlock() }
			case "foreign-source":
				if err := database.GetDB().Model(&model.ClientPolicySource{}).Where("node_key = ?", "local").Update("instance_id", "foreign-renewal-source").Error; err != nil {
					t.Fatal(err)
				}
				restore = func() {
					_ = database.GetDB().Model(&model.ClientPolicySource{}).Where("node_key = ?", "local").Update("instance_id", owner.state.SourceID).Error
				}
			}
			manager := panelruntime.GetManager()
			measured := &measuredResetRuntime{Runtime: manager.Local(), ManagedProcessRuntime: manager.Local().(panelruntime.ManagedProcessRuntime)}
			manager.SetLocalRuntimeOverride(measured)
			var renewalErr error
			if kind == "queued-cancellation" {
				base, abort := context.WithCancel(ctx)
				defer abort()
				queued := &directResetQueuedContext{Context: base, checked: make(chan struct{})}
				lock.Lock()
				done := make(chan error, 1)
				go func() {
					done <- applyAuthorityClientRenewalBatch(queued, process, ids, time.Now().UnixMilli(), time.UTC)
				}()
				select {
				case <-queued.checked:
				case <-ctx.Done():
					lock.Unlock()
					t.Fatal("renewal did not reach its initial context check")
				}
				abort()
				lock.Unlock()
				renewalErr, want = <-done, context.Canceled
			} else {
				renewalErr = applyAuthorityClientRenewalBatch(ctx, process, ids, time.Now().UnixMilli(), time.UTC)
			}
			restore()
			manager.SetLocalRuntimeOverride(nil)
			if !errors.Is(renewalErr, want) || !process.IsRunning() || measured.checkpoints != 0 || measured.applications != 0 {
				t.Fatalf("renewal admission interrupted healthy core: %v running=%t checkpoints=%d applications=%d", renewalErr, process.IsRunning(), measured.checkpoints, measured.applications)
			}
			page, err := owner.state.Journal.ResetOperationPage("", 128)
			if err != nil || len(page) != 0 {
				t.Fatalf("rejected renewal retained operation: %d/%v", len(page), err)
			}
			after, err := owner.state.Journal.Account(client.StableID)
			if err != nil || before != after {
				t.Fatalf("rejected renewal changed complete account: %v", err)
			}
			managedActivationEcho(t, flow, "stay")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			after, err = owner.state.Journal.Account(client.StableID)
			if err != nil || after.Usage.BilledBytes != 332 || after.WindowBaseline != before.WindowBaseline || after.Policy != before.Policy {
				t.Fatalf("rejected renewal lost continuing2x traffic: %+v/%v", after, err)
			}
		})
	}
}

func TestManagedAuthorityRenewalExhaustedCatchUpDoesNotOpenWindow(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	before, err := owner.state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	past := setManagedRenewalDue(t, client, 3, 2)
	if err := renewLocalClientPolicies(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	page, err := owner.state.Journal.ResetOperationPage("", 128)
	if err != nil || len(page) != 1 {
		t.Fatalf("expiry-only renewal missing capture: %d/%v", len(page), err)
	}
	capture, err := owner.state.Journal.LookupResetOperation(page[0].RequestID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := decodeAuthorityRenewalPreparation(prepared, capture, owner.state.Journal, owner.state.SourceID)
	if err != nil || len(snapshot.Resets) != 0 || len(snapshot.Effects) != 1 || snapshot.Effects[0].AfterExpiryTime != past+2*86400000 || snapshot.Effects[0].AfterResetCount != 2 {
		t.Fatalf("cap-exhausted catch-up changed original effect: %+v/%v", snapshot, err)
	}
	after, err := owner.state.Journal.Account(client.StableID)
	if err != nil || after.Usage != before.Usage || after.WindowBaseline != before.WindowBaseline || after.Policy.WindowID != before.Policy.WindowID || after.WindowUsed != before.WindowUsed {
		t.Fatalf("expired catch-up opened a window: %+v/%v", after, err)
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expired catch-up created semantic reset: %d/%v", count, err)
	}
	if err := renewLocalClientPolicies(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	if row := trafficOf(t, client.Email); row.ResetCount != 2 || row.ExpiryTime != past+2*86400000 {
		t.Fatalf("exhausted renewal spent another allowance: %+v", row)
	}
}
