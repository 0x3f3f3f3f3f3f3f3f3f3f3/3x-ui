package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
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
