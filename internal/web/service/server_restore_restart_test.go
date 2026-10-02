package service

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

func TestDatabaseRestoreOwnedRestartKeepsRealTunnelIdentityAndLedger(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	svc, tunnel, client, _ := setupManagedActivationService(t)
	// The managed fixture uses a short runtime directory for its Unix socket.
	// Move its initial SQL snapshot there before starting the real core so the
	// subsequent import replaces the database used by this fixture.
	if err := database.BackupSQLite(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		if process := currentXrayProcess(); process != nil {
			t.Fatalf("initial fixture startup: %v; child result: %s; control path bytes: %d", err, process.GetResult(), len(filepath.Join(config.GetDBFolderPath(), "client-policy", "control.sock")))
		}
		t.Fatal(err)
	}
	previous := currentXrayProcess()
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flow.Close() })
	managedActivationEcho(t, flow, "before")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	before := policyLedgerTotal(t, client.StableID)
	backup := filepath.Join(t.TempDir(), "incoming.db")
	if err := database.BackupSQLite(backup); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&ServerService{xrayService: *svc}).ImportDB(restoreUpload{bytes.NewReader(payload)}, false); err != nil {
		t.Fatalf("owned restore could not restart its real core: %v", err)
	}
	if currentXrayProcess() == previous || !currentXrayProcess().IsRunning() {
		t.Fatal("successful restore did not activate a replacement core")
	}
	managedActivationClosed(t, flow)
	afterFlow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer afterFlow.Close()
	managedActivationEcho(t, afterFlow, "after")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	current, err := (&ClientService{}).GetRecordByEmail(nil, client.Email)
	if err != nil || current.StableID != client.StableID {
		t.Fatalf("restore changed canonical identity: current=%+v err=%v", current, err)
	}
	if after := policyLedgerTotal(t, client.StableID); after.RawUpload != before.RawUpload+5 || after.RawDownload != before.RawDownload+5 || after.BilledBytes != before.BilledBytes+20 {
		t.Fatalf("restore lost or repeated admitted history: before=%+v after=%+v", before, after)
	}
}

func TestDatabaseRestoreRejectsForcedRestartBeforeReplacement(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	// A failed baseline must never launch a host or production executable.
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	setupPolicyLedgerDB(t)
	input := filepath.Join(t.TempDir(), "incoming.db")
	if err := database.BackupSQLite(input); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	previous := currentXrayProcess()
	oldManual, oldNeed := isManuallyStopped.Load(), isNeedXrayRestart.Load()
	oldStop, oldRestart := stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore
	t.Cleanup(func() {
		stopXrayBeforeDatabaseRestore, restartXrayAfterDatabaseRestore = oldStop, oldRestart
		xrayState.replace(previous)
		isManuallyStopped.Store(oldManual)
		isNeedXrayRestart.Store(oldNeed)
	})
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	stopXrayBeforeDatabaseRestore = func(*ServerService) error { close(started); <-release; return nil }
	restartXrayAfterDatabaseRestore = func(*ServerService, *databaseRestoreOwner) error { return nil }
	imported := make(chan error, 1)
	go func() { imported <- (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(payload)}, false) }()
	waitTrafficWriterSignal(t, started, "restore did not reach the stop/replacement boundary")
	if err := (&XrayService{}).RestartXray(true); !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Errorf("forced restart did not reject the active restore before runtime work: %v", err)
	}
	if currentXrayProcess() != previous {
		t.Error("forced restart replaced the tracked process during database restore")
	}
	releaseOnce.Do(func() { close(release) })
	if err := waitTrafficWriterErr(t, imported); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseRestoreRetiredOwnerCannotRestartAnotherImport(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	first, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	first.release()
	second, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer second.release()
	if err := (&ServerService{}).restartCoreAfterDatabaseRestore(first); !errors.Is(err, errDatabaseRestoreOwnerExpired) {
		t.Fatalf("retired restore owner could enter another import's runtime: %v", err)
	}
	first.release()
	if err := (&XrayService{}).RestartXray(true); !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("retired release removed the current import's restart fence: %v", err)
	}
}
