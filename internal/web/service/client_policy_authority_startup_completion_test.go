package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

type startupOperationFixture struct {
	svc      *XrayService
	inbound  *model.Inbound
	client   *model.ClientRecord
	config   conf.ClientPolicyConfig
	prepared policyauthority.ResetOperationPreparation
}

// Replaying the original policy instead of checking the current compiled one
// would rewind this later multiplier and charge the next payload at 2x.
func TestManagedAuthorityStartupAcknowledgementPreservesLaterState(t *testing.T) {
	t.Run("later-multiplier-window-inbound", testStartupLaterState)
	t.Run("deleted-uuid-reused-email-and-id", testStartupDeletedIdentity)
}

func testStartupLaterState(t *testing.T) {
	f := setupStartupPreparedOperation(t, "inbound")
	db := database.GetDB()
	if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Update("policy_multiplier", "0.5").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(f.inbound).Updates(map[string]any{"remark": "later startup configuration", "up": 701, "down": 907}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := owner.api.GetClient(ctx, f.client.StableID)
	if err != nil || state.Policy.MultiplierMicros != 500000 || state.Policy.Version != 2 {
		t.Fatalf("later0.5 policy lost: %+v/%v", state, err)
	}
	var inbound model.Inbound
	if err := db.First(&inbound, f.inbound.Id).Error; err != nil || inbound.Remark != "later startup configuration" || inbound.Up != 701 || inbound.Down != 907 {
		t.Fatalf("later inbound state lost: %+v/%v", inbound, err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", f.inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "next")
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	account, err := owner.state.Journal.Account(f.client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 108, RawDownload: 208, BilledBytes: 320}) || account.WindowBaseline != 316 || account.WindowUsed != 4 {
		t.Fatalf("later0.5 billing lost: %+v/%v", account, err)
	}
	if err := ResetLocalClientPolicy(ctx, f.client.StableID, "startup-later-window"); err != nil {
		t.Fatal(err)
	}
	_ = flow.Close()
	if err := f.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner = managedAuthorityForProcess(currentXrayProcess())
	account, err = owner.state.Journal.Account(f.client.StableID)
	if err != nil || account.WindowBaseline != 320 || account.WindowUsed != 0 {
		t.Fatalf("original operation rewound later window: %+v/%v", account, err)
	}
	prepared, err := owner.state.Journal.LookupResetPreparation(f.prepared.RequestID)
	if err != nil || prepared != f.prepared {
		t.Fatalf("later state changed original preparation: %v", err)
	}
	if _, err := owner.state.Journal.LookupResetCompletion(f.prepared.RequestID); err != nil {
		t.Fatal(err)
	}
}

func testStartupDeletedIdentity(t *testing.T) {
	f := setupStartupPreparedOperation(t, "direct")
	db := database.GetDB()
	if err := db.Create(&model.ClientPolicyTombstone{ClientID: f.client.StableID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM client_inbounds WHERE client_id = ?", f.client.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(f.client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("email = ?", f.client.Email).Delete(&panelxray.ClientTraffic{}).Error; err != nil {
		t.Fatal(err)
	}
	replacement := model.ClientRecord{Id: f.client.Id, Email: f.client.Email, SubID: "startup-replacement", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := db.Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	if replacement.StableID == f.client.StableID {
		t.Fatal("replacement reused UUID")
	}
	if err := db.Create(&panelxray.ClientTraffic{Email: replacement.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: replacement.Id, InboundId: f.inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	if err := setTunnelOwnerClients(f.inbound, &replacement); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(f.inbound).Update("settings", f.inbound.Settings).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	old, err := owner.state.Journal.Account(f.client.StableID)
	if err != nil || !old.Deleted || old.Usage != (policyauthority.Usage{RawUpload: 104, RawDownload: 204, BilledBytes: 316}) {
		t.Fatalf("deleted history lost: %+v/%v", old, err)
	}
	newAccount, err := owner.state.Journal.Account(replacement.StableID)
	if err != nil || newAccount.Usage != (policyauthority.Usage{RawUpload: 7, RawDownload: 11, BilledBytes: 18}) || newAccount.WindowBaseline != 18 || newAccount.WindowUsed != 18 || newAccount.Policy.Version != 1 || newAccount.Policy.WindowID != "initial:"+replacement.StableID {
		t.Fatalf("old boundary transferred to replacement: %+v/%v", newAccount, err)
	}
	if _, err := owner.state.Journal.LookupResetCompletion(f.prepared.RequestID); err != nil {
		t.Fatal(err)
	}
	prepared, err := owner.state.Journal.LookupResetPreparation(f.prepared.RequestID)
	if err != nil || prepared != f.prepared {
		t.Fatalf("deletion changed original preparation: %v", err)
	}
}

// Skipping post-RPC ownership/source checks or ignoring completion errors would
// leave a listener serving while its durable operation remains uncertain.
func TestManagedAuthorityStartupAcknowledgementRefusesUncertainExecution(t *testing.T) {
	for _, fault := range []string{"source", "pool", "socket", "boot", "core", "policy", "stamp", "null-stamp", "completion", "late-boot", "owner"} {
		t.Run(fault, func(t *testing.T) {
			kind := "direct"
			if fault == "stamp" || fault == "null-stamp" {
				kind = "inbound"
			}
			f := setupStartupPreparedOperation(t, kind)
			db := database.GetDB()
			injected := errors.New("startup final source unavailable")
			queries := 0
			var reached bool
			const callback = "test:startup-completion-fault"
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				process := currentXrayProcess()
				if process == nil || !process.IsControlReady() || tx.Statement.Table != "client_policy_sources" {
					return
				}
				queries++
				late := fault == "completion" || fault == "late-boot"
				if late && queries != 2 || !late && queries != 1 {
					return
				}
				reached = true
				owner := managedAuthorityForProcess(process)
				switch fault {
				case "source":
					owner.config.InstanceID = "foreign-startup-source"
				case "stamp", "null-stamp":
					var at any = 0
					if fault == "null-stamp" {
						at = nil
					}
					tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Model(&model.Inbound{}).Where("stable_id = ?", f.inbound.StableID).Update("last_traffic_reset_time", at).Error)
				case "pool":
					owner.db = db.Session(&gorm.Session{NewDB: true})
				case "owner":
					owner.process = nil
				case "socket":
					if err := os.Rename(owner.socketPath, owner.socketPath+".startup-fault"); err != nil {
						tx.AddError(err)
						return
					}
					listener, err := net.Listen("unix", owner.socketPath)
					if err != nil {
						tx.AddError(err)
						return
					}
					defer listener.Close()
				case "boot", "late-boot":
					owner.socketBoot = "foreign-startup-boot"
				case "core":
					tx.AddError(owner.api.Close())
				case "policy":
					compiled, err := owner.config.Build()
					if err != nil {
						tx.AddError(err)
						return
					}
					compiled.Policies[0].Version++
					compiled.Policies[0].MultiplierMicros = 3000000
					tx.AddError(owner.api.Apply(tx.Statement.Context, compiled.Policies))
				case "completion":
					tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			err := f.svc.RestartXray(true)
			if removeErr := db.Callback().Query().Remove(callback); removeErr != nil {
				t.Fatal(removeErr)
			}
			process := currentXrayProcess()
			if err == nil || !reached || process.IsRunning() {
				t.Fatalf("uncertain startup remained open: %v reached=%t running=%t queries=%d", err, reached, process.IsRunning(), queries)
			}
			if fault == "completion" && !errors.Is(err, injected) {
				t.Fatalf("lost completion failure: %v", err)
			}
			if fault == "pool" && !errors.Is(err, ErrDatabaseReplaced) {
				t.Fatalf("foreign SQL handle accepted: %v", err)
			}
			conn, dialErr := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", f.inbound.Port), time.Second)
			if dialErr == nil {
				conn.Close()
				t.Fatal("failed startup listener remained reachable")
			}
			state, openErr := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer state.Journal.Close()
			if _, err := state.Journal.LookupResetCompletion(f.prepared.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("uncertain execution acknowledged: %v", err)
			}
			prepared, err := state.Journal.LookupResetPreparation(f.prepared.RequestID)
			if err != nil || prepared != f.prepared {
				t.Fatalf("fault changed original preparation: %v", err)
			}
		})
	}
	t.Run("cancelled-before-completion", func(t *testing.T) {
		svc, _, _, _ := setupManagedActivationService(t)
		if err := svc.RestartXray(true); err != nil {
			t.Fatal(err)
		}
		owner := managedAuthorityForProcess(currentXrayProcess())
		lock.Lock()
		defer lock.Unlock()
		prepared := startupZeroEffectProgram(t, owner.state, "startup-cancelled-pending", "bulk", true)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		count := 0
		const callback = "test:startup-completion-cancellation"
		db := database.GetDB()
		if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "client_policy_sources" {
				count++
				if count == 2 {
					cancel()
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
		err := owner.CompleteStartupOperations(ctx)
		if removeErr := db.Callback().Query().Remove(callback); removeErr != nil {
			t.Fatal(removeErr)
		}
		if !errors.Is(err, context.Canceled) || count != 2 {
			t.Fatalf("cancellation at completion ignored: %v/%d", err, count)
		}
		if _, err := owner.state.Journal.LookupResetCompletion(prepared.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
			t.Fatalf("cancelled completion persisted: %v", err)
		}
	})
}

func setupStartupPreparedOperation(t *testing.T, kind string) startupOperationFixture {
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
			t.Fatalf("FK enforcement absent: %d/%v", enabled, err)
		}
	}
	const callback = "test:startup-prepared-commit-failure"
	installDeferredCommitFailure(t, db, "create", callback, "client_policy_resets", "startup_operation_parent", "startup_operation_child")
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	process := currentXrayProcess()
	cfg := managedAuthorityForProcess(process).config
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request := "startup-prepared-" + kind
	switch kind {
	case "direct":
		err = ResetLocalClientPolicy(ctx, client.StableID, request)
	case "batch":
		_, err = (&ClientService{}).ResetAllTrafficsWithRequest(ctx, request)
	case "inbound":
		err = (&ClientService{}).ResetAllClientTrafficsWithRequest(ctx, &InboundService{}, inbound.Id, request)
	case "renewal":
		setManagedRenewalDue(t, client, 0, 2)
		err = renewLocalClientPolicies(ctx, process)
	default:
		t.Fatalf("unknown startup fixture %s", kind)
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") || process.IsRunning() {
		t.Fatalf("not actual failed commit/stopped core: %v/%t", err, process.IsRunning())
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	page, err := state.Journal.ResetOperationPage("", 128)
	if err != nil || len(page) != 1 {
		t.Fatalf("missing unique interrupted operation: %d/%v", len(page), err)
	}
	prepared, err := state.Journal.LookupResetPreparation(page[0].RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Journal.LookupResetCompletion(prepared.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("failed execution acquired completion: %v", err)
	}
	return startupOperationFixture{svc: svc, inbound: inbound, client: client, config: cfg, prepared: prepared}
}

// Skipping completion after successful owned startup leaves a durable
// preparation unresolved even though the real core applies its reset window.
func TestManagedAuthorityStartupCompletesPreparedOperations(t *testing.T) {
	for _, kind := range []string{"direct", "batch", "renewal", "inbound"} {
		t.Run(kind, func(t *testing.T) {
			f := setupStartupPreparedOperation(t, kind)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := recoverAuthorityDesiredState(ctx, &f.config); err != nil {
				t.Fatal(err)
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := state.Journal.LookupResetCompletion(f.prepared.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("metadata-only recovery acknowledged core: %v", err)
			}
			if err := state.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			owner := managedAuthorityForProcess(currentXrayProcess())
			done, err := owner.state.Journal.LookupResetCompletion(f.prepared.RequestID)
			if err != nil {
				t.Fatalf("actual successful startup left original preparation uncompleted: %v", err)
			}
			if done.PreparationDigest != authorityResetSnapshotDigest(f.prepared.Snapshot) {
				t.Fatal("startup completion lost original preparation binding")
			}
			flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", f.inbound.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			managedActivationEcho(t, flow, "next")
			managedActivationEcho(t, flow, "stay")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			before, err := owner.state.Journal.Account(f.client.StableID)
			if err != nil || before.Usage != (policyauthority.Usage{RawUpload: 112, RawDownload: 212, BilledBytes: 348}) || before.WindowBaseline != 316 || before.WindowUsed != 32 {
				t.Fatalf("startup changed original 2x boundary: %+v/%v", before, err)
			}
			_ = flow.Close()
			if err := f.svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			owner = managedAuthorityForProcess(currentXrayProcess())
			retained, err := owner.state.Journal.LookupResetPreparation(f.prepared.RequestID)
			if err != nil || retained != f.prepared {
				t.Fatalf("repeated startup rewrote original preparation: %v", err)
			}
			completed, err := owner.state.Journal.LookupResetCompletion(f.prepared.RequestID)
			if err != nil || completed != done {
				t.Fatalf("repeated startup rewrote completion: %v", err)
			}
			after, err := owner.state.Journal.Account(f.client.StableID)
			if err != nil || after.Usage != before.Usage || after.WindowBaseline != before.WindowBaseline || after.WindowUsed != before.WindowUsed || after.Policy.WindowID != before.Policy.WindowID {
				t.Fatalf("repeated startup reset accounted window: %+v/%v", after, err)
			}
		})
	}
}
