package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type directResetQueuedContext struct {
	context.Context
	first   sync.Once
	checked chan struct{}
}

func (c *directResetQueuedContext) Err() error {
	err := c.Context.Err()
	c.first.Do(func() { close(c.checked) })
	return err
}

// Installing cleanup before admission would stop this unrelated live flow.
func TestManagedAuthorityDirectResetRejectedAdmissionKeepsLiveCore(t *testing.T) {
	for _, kind := range []string{"queued-cancellation", "unknown-uuid"} {
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
			manager := panelruntime.GetManager()
			measured := &measuredResetRuntime{Runtime: manager.Local(), ManagedProcessRuntime: manager.Local().(panelruntime.ManagedProcessRuntime)}
			manager.SetLocalRuntimeOverride(measured)
			defer manager.SetLocalRuntimeOverride(nil)
			var resetErr error
			switch kind {
			case "queued-cancellation":
				base, abort := context.WithCancel(ctx)
				defer abort()
				queued := &directResetQueuedContext{Context: base, checked: make(chan struct{})}
				lock.Lock()
				done := make(chan error, 1)
				go func() { done <- ResetLocalClientPolicy(queued, client.StableID, "queued-rejected-reset") }()
				select {
				case <-queued.checked:
				case <-ctx.Done():
					lock.Unlock()
					t.Fatal("queued request did not reach its initial context check")
				}
				abort()
				lock.Unlock()
				resetErr = <-done
				if !errors.Is(resetErr, context.Canceled) {
					t.Fatalf("queued cancellation: %v", resetErr)
				}
			case "unknown-uuid":
				resetErr = ResetLocalClientPolicies(ctx, []string{client.StableID, uuid.NewString()}, "unknown-rejected-reset")
				if !errors.Is(resetErr, policyauthority.ErrNotFound) {
					t.Fatalf("unknown identity admission: %v", resetErr)
				}
			}
			if !process.IsRunning() || managedAuthorityForProcess(process) != owner || measured.checkpoints != 0 || measured.applications != 0 {
				t.Fatalf("pre-execution rejection interrupted live service: running=%t checkpoints=%d applies=%d err=%v", process.IsRunning(), measured.checkpoints, measured.applications, resetErr)
			}
			page, err := owner.state.Journal.ResetOperationPage("", 128)
			if err != nil || len(page) != 0 {
				t.Fatalf("rejected request captured an operation: %d/%v", len(page), err)
			}
			var count int64
			if err := database.GetDB().Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("rejected request changed SQL windows: %d/%v", count, err)
			}
			after, err := owner.state.Journal.Account(client.StableID)
			if err != nil || before.Policy != after.Policy || before.Usage != after.Usage || before.WindowBaseline != after.WindowBaseline || before.WindowUsed != after.WindowUsed {
				t.Fatalf("rejected request changed accounting: %v", err)
			}
			manager.SetLocalRuntimeOverride(nil)
			managedActivationEcho(t, flow, "stay")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			after, err = owner.state.Journal.Account(client.StableID)
			if err != nil || after.Usage != (policyauthority.Usage{RawUpload: 108, RawDownload: 208, BilledBytes: 332}) || after.Policy != before.Policy {
				t.Fatalf("unrelated live flow did not continue exact2x billing: %+v/%v", after, err)
			}
		})
	}
}

// Narrowing admission cleanup must still stop a genuinely applied reset when
// its completion cannot be persisted. The final source query follows core RPC.
func TestManagedAuthorityDirectResetCompletionFailureStopsAppliedCore(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	db := database.GetDB()
	injected := errors.New("direct completion source validation unavailable")
	var armed, reached atomic.Bool
	if err := db.Callback().Query().Before("gorm:query").Register("test:direct-completion-failure", func(tx *gorm.DB) {
		if armed.Load() && tx.Statement.Table == "client_policy_sources" {
			reached.Store(true)
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		armed.Store(false)
		if process := currentXrayProcess(); process != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = stopManagedAuthority(ctx, process)
			cancel()
		}
		if err := db.Callback().Query().Remove("test:direct-completion-failure"); err != nil {
			t.Error(err)
		}
	})
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	manager := panelruntime.GetManager()
	measured := &measuredResetRuntime{Runtime: manager.Local(), ManagedProcessRuntime: manager.Local().(panelruntime.ManagedProcessRuntime), beforeApply: func() { armed.Store(true) }}
	manager.SetLocalRuntimeOverride(measured)
	err := ResetLocalClientPolicy(context.Background(), client.StableID, "completion-failed-direct")
	manager.SetLocalRuntimeOverride(nil)
	armed.Store(false)
	if !errors.Is(err, injected) || !reached.Load() || measured.applications != 1 || process.IsRunning() {
		t.Fatalf("applied completion failure did not close service: %v reached=%t applies=%d running=%t", err, reached.Load(), measured.applications, process.IsRunning())
	}
	key := authorityDirectResetKey("completion-failed-direct", []string{client.StableID})
	state, err := openAuthorityState(filepath.Join(filepath.Dir(owner.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	if _, err := state.Journal.LookupResetPreparation(key); err != nil {
		t.Fatalf("completion failure lost exact preparation: %v", err)
	}
	if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("failed completion was acknowledged: %v", err)
	}
}

// A full IN list exceeds SQLite's parameter bound. Validate every bounded scope
// chunk before account/history lookup; a remote member in the last chunk must
// refuse atomically. This is SQL admission evidence, not 40000-client execution.
func TestManagedAuthorityDirectResetCaptureBoundsScopeAdmission(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote-last-%t", remote), func(t *testing.T) {
			svc, _, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			owner := managedAuthorityForProcess(currentXrayProcess())
			db := database.GetDB()
			last := model.ClientRecord{StableID: "ffffffff-ffff-4fff-bfff-ffffffffffff", Email: "scope-bound-last", Enable: true}
			if err := db.Create(&last).Error; err != nil {
				t.Fatal(err)
			}
			if remote {
				node := model.Node{Name: "scope-bound-node"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				inbound := mkInbound(t, 0, model.Tunnel, `{"clients":[]}`)
				if err := db.Model(inbound).Update("node_id", node.Id).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec("INSERT INTO client_inbounds (client_id, inbound_id) VALUES (?, ?)", last.Id, inbound.Id).Error; err != nil {
					t.Fatal(err)
				}
			}
			ids := make([]string, 40000)
			for i := 0; i < len(ids)-2; i++ {
				ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1)
			}
			ids[len(ids)-2], ids[len(ids)-1] = client.StableID, last.StableID
			ids, err := clientPolicyResetIDs(ids)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			lock.Lock()
			err = runSerializedTxContextForDatabase(ctx, db, func(tx *gorm.DB) error {
				_, err := captureAuthorityDirectResetTx(tx.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}), owner.state, ids, "scope-bound-rejection")
				return err
			})
			lock.Unlock()
			want := policyauthority.ErrNotFound
			if remote {
				want = ErrClientPolicyLedger
			}
			if !errors.Is(err, want) {
				t.Fatalf("scope admission failed before bounded account/remote rejection: %v, want %v", err, want)
			}
			page, err := owner.state.Journal.ResetOperationPage("", 128)
			if err != nil || len(page) != 0 {
				t.Fatalf("invalid scope acquired a capture: %d/%v", len(page), err)
			}
		})
	}
}
