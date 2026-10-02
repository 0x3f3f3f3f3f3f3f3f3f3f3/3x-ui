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

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/infra/conf"
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
