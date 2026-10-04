package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

func TestManagedPolicyCoordinatorLifecycleDrainsBeforeRestoreWithAbsentCore(t *testing.T) {
	setupPolicyLedgerDB(t)
	ctx := context.Background()
	t.Logf("managed coordinator production lifecycle backend: %s", database.GetDB().Dialector.Name())
	previous := currentXrayProcess()
	xrayState.replace(nil)
	t.Cleanup(func() { xrayState.replace(previous); _ = StopManagedPolicyCoordinator(ctx) })
	c, err := getManagedPolicyCoordinator(ctx, false)
	if err != nil || c != nil {
		t.Fatal("inactive panel created coordinator", err)
	}
	dir := filepath.Join(config.GetDBFolderPath(), "client-policy", "coordinator")
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inactive startup created original state")
	}
	c, err = getManagedPolicyCoordinator(ctx, true)
	if err != nil || c == nil {
		t.Fatal("explicit activation did not retain coordinator", err)
	}
	original := c.state.Journal.Identity()
	if same, err := getManagedPolicyCoordinator(ctx, true); err != nil || same != c {
		t.Fatal("production accessor acquired a second owner", err)
	}
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	if err := (&ServerService{}).stopCoreForDatabaseRestore(); err != nil {
		t.Fatal(err)
	}
	if !c.closed {
		t.Fatal("confirmed absent local core skipped live coordinator drain")
	}
	if _, err := getManagedPolicyCoordinator(ctx, true); !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("restore admitted a new coordinator: %v", err)
	}
	owner.release()
	c, err = getManagedPolicyCoordinator(ctx, false)
	if err != nil || c == nil || c.state.Journal.Identity() != original {
		t.Fatal("activated resume lost original control identity", err)
	}
	if _, err := getManagedPolicyCoordinator(nil, true); err == nil {
		t.Fatal("nil lifecycle context admitted owner")
	}
	if err := StopManagedPolicyCoordinator(ctx); err != nil || !c.closed {
		t.Fatal("panel stop did not close tracked coordinator", err)
	}
}

func TestManagedPolicyCoordinatorRestoreReleaseResumesOriginalOwner(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc, _, _, _ := setupManagedActivationServiceWithUsage(t, 0, 0)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Cleanup(func() { _ = StopManagedPolicyCoordinator(ctx) })
	t.Logf("managed coordinator actual restore release backend: %s", database.GetDB().Dialector.Name())
	c, err := getManagedPolicyCoordinator(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	original := c.state.Journal.Identity()
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	server := &ServerService{}
	if err := server.stopCoreForDatabaseRestore(); err != nil || !c.closed {
		t.Fatal("actual restore did not drain coordinator/core", err)
	}
	if err := server.restartCoreAfterDatabaseRestore(owner); err != nil {
		t.Fatal("actual core restart under restore owner failed", err)
	}
	owner.release()
	managedCoordinatorOwner.Lock()
	resumed := managedCoordinatorOwner.coordinator
	managedCoordinatorOwner.Unlock()
	if resumed == nil || resumed == c || resumed.state.Journal.Identity() != original {
		t.Fatal("successful restore release did not automatically resume original coordinator")
	}
}
