package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

func TestManagedAuthorityOrdinaryRestartAndStopUseDurableController(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "live")
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	if process := currentXrayProcess(); process != nil && process.IsRunning() {
		t.Fatal("ordinary stop retained business core")
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err := state.Journal.Account(client.StableID)
	if err != nil || a.Usage != (policyauthority.Usage{RawUpload: 104, RawDownload: 204, BilledBytes: 316}) || a.HeldCapacity != 0 {
		t.Fatalf("ordinary service authority accounting: %+v/%v", a, err)
	}
}

func TestManagedAuthorityUncertainCloseRequiresStoppedCoreAndRetainsHolds(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	authority := managedAuthorityForProcess(process)
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "hold")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := authority.controller.join(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := authority.state.Journal.Account(client.StableID)
	if err != nil || before.HeldCapacity == 0 {
		t.Fatalf("missing outstanding allocation: %+v/%v", before, err)
	}
	if closed, err := authority.closeStopped(ctx); closed || !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("live core lost its authority owner: %v/%v", closed, err)
	}
	if err := process.Stop(); err != nil {
		t.Fatal(err)
	}
	if closed, err := authority.closeStopped(ctx); !closed || err != nil {
		t.Fatalf("stopped core retained its owner: %v/%v", closed, err)
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	after, err := state.Journal.Account(client.StableID)
	if err != nil || after != before {
		t.Fatalf("uncertain close credited an unsealed allocation: %+v/%v", after, err)
	}
}

func TestManagedAuthorityUnexpectedCoreExitRetainsBudgetAndCanRestart(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := database.GetDB().Model(client).Update("total_gb", 12<<20).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	old := currentXrayProcess()
	authority := managedAuthorityForProcess(old)
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := authority.controller.SettleAndRenew(ctx); err != nil {
		t.Fatal(err)
	}
	if err := authority.controller.join(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := authority.state.Journal.Account(client.StableID)
	if err != nil || before.HeldCapacity == 0 {
		t.Fatalf("missing outstanding grant: %+v/%v", before, err)
	}
	// Kill only this test-owned child, without a graceful core checkpoint or
	// the panel's authority stop path.
	child, err := os.FindProcess(old.PID())
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for old.IsRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if old.IsRunning() {
		t.Fatal("test-owned core exit was not confirmed")
	}
	if err := svc.RestartXray(true); err != nil {
		result := ""
		if failed := currentXrayProcess(); failed != nil {
			result = failed.GetResult()
		}
		t.Fatalf("confirmed core exit recovery: %v; core: %s", err, result)
	}
	next := managedAuthorityForProcess(currentXrayProcess())
	archived, archiveErr := os.Lstat(authority.socketPath + ".retired-" + authority.socketBoot)
	if archiveErr != nil || !os.SameFile(archived, authority.socketInfo) {
		t.Fatalf("crashed core socket evidence was not preserved: %v", archiveErr)
	}
	if next == nil || next.controller.execution.boot == authority.controller.execution.boot {
		t.Fatal("core restart reused its old authorization incarnation")
	}
	after, err := next.state.Journal.Account(client.StableID)
	if err != nil || after.HeldCapacity != before.HeldCapacity || after.Usage != before.Usage {
		t.Fatalf("unsealed exit returned old quota: %+v/%v", after, err)
	}
	conn, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "next")
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	final, err := state.Journal.Account(client.StableID)
	if err != nil || final.HeldCapacity != before.HeldCapacity || final.Usage.BilledBytes != before.Usage.BilledBytes+16 {
		t.Fatalf("replacement settlement credited the unsealed predecessor: %+v/%v", final, err)
	}
}

func TestManagedAuthorityForcedRestartPreservesSettledAllowance(t *testing.T) {
	svc, tunnel, _, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	first := currentXrayProcess()
	t.Cleanup(func() {
		if authority := managedAuthorityForProcess(first); authority != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = authority.Stop(ctx)
			cancel()
			authority.mu.Lock()
			if authority.api != nil {
				_ = authority.api.Close()
			}
			_ = authority.state.Journal.Close()
			authority.closed = true
			authority.mu.Unlock()
			localAuthority.Lock()
			localAuthority.process, localAuthority.authority = nil, nil
			localAuthority.Unlock()
		}
	})
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, conn, "warm")
	_ = conn.Close()
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	if currentXrayProcess() == first {
		t.Fatal("force restart retained old boot")
	}
	conn, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "next")
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedAuthorityStoppedSocketRefusesReplacementAndExistingArchive(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement-%t", replacement), func(t *testing.T) {
			dir, err := os.MkdirTemp("", "authority-socket-")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("retained owned socket fixture: %s", dir)
			path := filepath.Join(dir, "control.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			listener.SetUnlinkOnClose(false)
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			a := &managedAuthority{socketPath: path, socketInfo: info, socketBoot: strings.Repeat("a", 32)}
			archive := path + ".retired-" + a.socketBoot
			if replacement {
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				defer listener.Close()
			} else if err := os.WriteFile(archive, []byte("retained evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.preserveStoppedSocket(context.Background()); !errors.Is(err, ErrClientPolicyLedger) {
				t.Fatalf("changed socket or existing archive was replaced: %v", err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("rejected socket cleanup changed an unowned path: %v", err)
			}
			if !replacement {
				if data, err := os.ReadFile(archive); err != nil || string(data) != "retained evidence" {
					t.Fatalf("existing socket evidence was overwritten: %q/%v", data, err)
				}
			}
		})
	}
}

func TestManagedAuthorityActivatedMissingStateIsNeverFirstInstall(t *testing.T) {
	path, _ := migratedAuthorityFixture(t)
	dir := filepath.Join(filepath.Dir(path), "authority")
	if err := os.Rename(dir, dir+".retained"); err != nil {
		t.Fatal(err)
	}
	config := &conf.ClientPolicyConfig{StateFile: path, InstanceID: "migration-source"}
	lock.Lock()
	err := initializeFreshAuthorityLocked(context.Background(), config)
	lock.Unlock()
	if !errors.Is(err, ErrAuthorityNotInitialized) {
		t.Fatalf("activated source recreated authority: %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected first-install created files: %v", err)
	}
}

func TestManagedAuthorityOrdinaryResetPreservesLifetimeAndReusesWindow(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ResetLocalClientPolicy(ctx, client.StableID, "ordinary-reset"); err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, conn, "next")
	if err := ResetLocalClientPolicy(ctx, client.StableID, "ordinary-reset"); err != nil {
		t.Fatal(err)
	}
	if currentXrayProcess() != process {
		t.Fatal("reset restarted the business core")
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err := state.Journal.Account(client.StableID)
	if err != nil || a.Usage != (policyauthority.Usage{RawUpload: 108, RawDownload: 208, BilledBytes: 332}) || a.WindowUsed != 16 || a.WindowBaseline != 316 || a.HeldCapacity != 0 {
		t.Fatalf("reset lost lifetime usage or recreated its window: %+v/%v", a, err)
	}
}

func TestManagedAuthorityPollingRetriesCommittedMultiplierWithoutResettingUsage(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "warm")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	// Simulate a committed SQL change whose normal hot-application reply was
	// lost. Ordinary polling must reconcile both enforcement and authority.
	if err := database.GetDB().Model(client).Update("policy_multiplier", "0.5").Error; err != nil {
		t.Fatal(err)
	}
	policies, err := PrepareClientPolicies([]string{client.StableID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	account, err := managedAuthorityForProcess(process).state.Journal.Account(client.StableID)
	if err != nil || account.Policy.Version != policies[0].Version {
		t.Fatalf("polling left committed authority version behind: %+v/%v", account, err)
	}
	managedActivationEcho(t, conn, "next")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if currentXrayProcess() != process {
		t.Fatal("pending policy reconciliation restarted the core")
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	account, err = state.Journal.Account(client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 108, RawDownload: 208, BilledBytes: 320}) || account.WindowUsed != 320 || account.HeldCapacity != 0 {
		t.Fatalf("pending multiplier changed previous accounting or left capacity held: %+v/%v", account, err)
	}
}

func TestManagedAuthorityPollingProjectionFailureStopsAndReplaysCommittedChange(t *testing.T) {
	testManagedAuthorityProjectionFailureStopsAndReplays(t, false)
}

func TestManagedAuthorityDirectPolicyProjectionFailureStopsAndReplaysCommittedChange(t *testing.T) {
	testManagedAuthorityProjectionFailureStopsAndReplays(t, true)
}

func testManagedAuthorityProjectionFailureStopsAndReplays(t *testing.T, direct bool) {
	t.Helper()
	svc, tunnel, client, _ := setupManagedActivationService(t)
	db := database.GetDB()
	injected := errors.New("pending authority projection failed")
	var failVersion atomic.Uint64
	var faultAuthority atomic.Pointer[managedAuthority]
	// Register before the authority worker starts. GORM's callback registry
	// cannot be changed while a background SQL projection is executing.
	if err := db.Callback().Update().Before("gorm:update").Register("test:pending-authority-projection", func(tx *gorm.DB) {
		version := failVersion.Load()
		if version == 0 || tx.Statement.Table != "client_policy_authority_projections" {
			return
		}
		authority := faultAuthority.Load()
		if authority == nil {
			return
		}
		account, err := authority.state.Journal.Account(client.StableID)
		if err == nil && account.Policy.Version == version {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	faultAuthority.Store(managedAuthorityForProcess(process))
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "warm")
	if err := db.Model(client).Update("policy_multiplier", "0.5").Error; err != nil {
		t.Fatal(err)
	}
	policies, err := PrepareClientPolicies([]string{client.StableID})
	if err != nil {
		t.Fatal(err)
	}
	failVersion.Store(policies[0].Version)
	var applyErr error
	if direct {
		applyErr = reconcileLocalClientPolicy(client.StableID)
	} else {
		_, _, applyErr = svc.GetXrayTraffic()
	}
	if !errors.Is(applyErr, injected) {
		t.Fatalf("pending projection failure hidden: %v", applyErr)
	}
	if process.IsRunning() {
		t.Fatal("failed authority application retained outdated access")
	}
	failVersion.Store(0)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	conn, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "next")
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	account, err := state.Journal.Account(client.StableID)
	if err != nil || account.Policy.Version != policies[0].Version || account.Usage != (policyauthority.Usage{RawUpload: 108, RawDownload: 208, BilledBytes: 320}) || account.HeldCapacity != 0 {
		t.Fatalf("pending change replay lost or recreated committed usage: %+v/%v", account, err)
	}
}

func TestManagedAuthorityAccountingSeparatesAllocatedBudgetFromDeliveredUsage(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "warm")
	authority := managedAuthorityForProcess(currentXrayProcess())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := authority.controller.SettleAndRenew(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if err := authority.controller.join(ctx); err != nil {
		t.Fatal(err)
	}
	traffic, err := (&InboundService{}).GetClientTrafficByEmail(client.Email)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(traffic.Accounting)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Lifetime struct{ Upload, Download, Billed string }
		Budget   *struct{ Allocated, Frozen, Unallocated string }
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if view.Lifetime.Upload != "104" || view.Lifetime.Download != "204" || view.Lifetime.Billed != "316" || view.Budget == nil || view.Budget.Allocated != "9684" || view.Budget.Frozen != "0" || view.Budget.Unallocated != "0" {
		t.Fatalf("allocated budget was omitted or counted as delivered traffic: %s", raw)
	}
}

func TestManagedAuthorityPairedOldSnapshotsKeepConfirmedLifetimeVisible(t *testing.T) {
	testManagedAuthorityPairedOldSnapshots(t, 0)
}

func TestManagedAuthorityPairedOldSnapshotsRetainAppliedResetWindow(t *testing.T) {
	testManagedAuthorityPairedOldSnapshots(t, 1)
}

func TestManagedAuthorityPairedOldSnapshotsRetainEarlierResetRequests(t *testing.T) {
	testManagedAuthorityPairedOldSnapshots(t, 2)
}

func TestManagedAuthorityPendingResetChargesTrafficSinceCommittedBoundary(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "warm")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	policy, err := PrepareClientPolicyReset(owner.config.InstanceID, client.StableID, "delayed-application")
	if err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, conn, "post")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	account, err := owner.state.Journal.Account(client.StableID)
	if err != nil || account.Policy.Version != policy.Version || account.WindowUsed != 16 || account.Usage.BilledBytes != 332 {
		t.Fatalf("pending reset credited traffic after its committed boundary: %+v/%v", account, err)
	}
	traffic, err := (&InboundService{}).GetClientTrafficByEmail(client.Email)
	if err != nil || traffic.Accounting == nil || traffic.Accounting.Period.Billed != "16" {
		t.Fatalf("pending reset SQL/core boundary disagreed: %+v/%v", traffic, err)
	}
}

func testManagedAuthorityPairedOldSnapshots(t *testing.T, resetCount int) {
	t.Helper()
	// This acceptance exercises the actual SQLite import route. PostgreSQL
	// import/reconciliation has its own backend gate, rather than relabeling it.
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	svc, tunnel, client, _ := setupManagedActivationService(t)
	// The activation helper keeps SQL in its original isolated directory. The
	// real import route uses the configured deployment path, so publish an
	// identical fresh fixture there before any managed process has booted.
	if err := database.BackupSQLite(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	if err := database.CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	var coreConfig conf.ClientPolicyConfig
	if err := json.Unmarshal(currentXrayProcess().GetConfig().ClientPolicy, &coreConfig); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Dir(coreConfig.StateFile)
	sqlSnapshot := filepath.Join(fixture, "before-consumption-sql.db")
	if err := database.BackupSQLite(sqlSnapshot); err != nil {
		t.Fatal(err)
	}
	oldSQL, err := os.ReadFile(sqlSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	oldCore, err := os.ReadFile(coreConfig.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "before-consumption-core.db"), oldCore, 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, conn, "warm")
	_ = conn.Close()
	for resetIndex := range resetCount {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		requestID := "preserved-reset-window"
		if resetIndex > 0 {
			requestID = fmt.Sprintf("preserved-reset-window-%d", resetIndex+1)
		}
		if err := ResetLocalClientPolicy(ctx, client.StableID, requestID); err != nil {
			t.Fatal(err)
		}
		conn, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		managedActivationEcho(t, conn, "post")
		_ = conn.Close()
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	consumed, err := os.ReadFile(coreConfig.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "after-consumption-core.db"), consumed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := database.BackupSQLite(filepath.Join(fixture, "after-consumption-sql.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coreConfig.StateFile, oldCore, 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(oldSQL)}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	traffic, err := (&InboundService{}).GetClientTrafficByEmail(client.Email)
	if err != nil {
		t.Fatal(err)
	}
	upload, download, billed := fmt.Sprint(104+4*resetCount), fmt.Sprint(204+4*resetCount), fmt.Sprint(316+16*resetCount)
	if traffic.Accounting == nil || traffic.Accounting.Lifetime.Upload != upload || traffic.Accounting.Lifetime.Download != download || traffic.Accounting.Lifetime.Billed != billed {
		t.Fatalf("old SQL/core snapshots hid committed lifetime usage: %+v", traffic.Accounting)
	}
	if resetCount > 0 && (traffic.Accounting.Period.Billed != "16" || traffic.Accounting.ResetPending) {
		t.Fatalf("old snapshots reopened an obsolete quota window: %+v", traffic.Accounting)
	}
	if resetCount > 1 {
		var resets []model.ClientPolicyReset
		if err := database.GetDB().Where("client_id = ?", client.StableID).Order("id").Find(&resets).Error; err != nil {
			t.Fatal(err)
		}
		if len(resets) != resetCount {
			t.Fatalf("older applied reset request disappeared after snapshot restore: %+v", resets)
		}
		owner := managedAuthorityForProcess(currentXrayProcess())
		before, err := owner.state.Journal.Account(client.StableID)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := ResetLocalClientPolicy(ctx, client.StableID, "preserved-reset-window"); err != nil {
			t.Fatal(err)
		}
		after, err := owner.state.Journal.Account(client.StableID)
		if err != nil || before.Policy != after.Policy || before.Usage != after.Usage || before.WindowUsed != after.WindowUsed {
			t.Fatalf("old reset retry reopened a retained window: %+v/%+v/%v", before, after, err)
		}
	}
	conn, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "next")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	traffic, err = (&InboundService{}).GetClientTrafficByEmail(client.Email)
	if err != nil {
		t.Fatal(err)
	}
	upload, download, billed = fmt.Sprint(108+4*resetCount), fmt.Sprint(208+4*resetCount), fmt.Sprint(332+16*resetCount)
	if traffic.Accounting == nil || traffic.Accounting.Lifetime.Billed != billed || traffic.Accounting.Lifetime.Upload != upload || traffic.Accounting.Lifetime.Download != download {
		t.Fatalf("restored execution failed to preserve and advance known lifetime: %+v", traffic.Accounting)
	}
	if resetCount > 0 && traffic.Accounting.Period.Billed != "32" {
		t.Fatalf("new restored traffic used the wrong retained quota window: %+v", traffic.Accounting)
	}
}

func TestManagedAuthorityPollingKeepsLedgerReceiptWhenGrantCheckpointFails(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "warm")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	authority := managedAuthorityForProcess(currentXrayProcess())
	c := authority.controller
	injected := errors.New("one grant checkpoint is unavailable")
	c.mu.Lock()
	original := c.execution.api
	c.execution.api = &authorityDemandFault{authorityDemandAPI: c.api, settleClient: client.StableID, settleFault: injected}
	c.mu.Unlock()
	t.Cleanup(func() { c.mu.Lock(); c.execution.api = original; c.mu.Unlock() })
	managedActivationEcho(t, conn, "next")
	if _, _, err := svc.GetXrayTraffic(); !errors.Is(err, injected) {
		t.Fatalf("checkpoint failure hidden: %v", err)
	}
	var receipt model.ClientPolicyReceipt
	if err := database.GetDB().First(&receipt, "client_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.RawUpload != 108 || receipt.RawDownload != 208 || receipt.BilledBytes != 332 {
		t.Fatalf("grant checkpoint fault blocked the independent ledger collection: %+v", receipt)
	}
	c.mu.Lock()
	c.execution.api = original
	c.mu.Unlock()
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedAuthorityRestoreOwnerSealsWhilePublicStopIsFenced(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managedActivationEcho(t, conn, "seal")
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopXray(); !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("public stop bypassed restore owner: %v", err)
	}
	if !currentXrayProcess().IsRunning() {
		t.Fatal("fenced public stop terminated restore-owned core")
	}
	if err := (&ServerService{}).stopCoreForDatabaseRestore(); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err := state.Journal.Account(client.StableID)
	if err != nil || a.Usage.BilledBytes != 316 || a.HeldCapacity != 0 {
		t.Fatalf("restore owner failed to settle through its SQL fence: %+v/%v", a, err)
	}
}
