package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"gorm.io/gorm"
)

// Returning after the first header page or replaying already completed work
// would miss the last preparation or change previously committed accounting.
// These129 private zero-effect programs are storage traversal evidence only.
func TestManagedAuthorityStartupAcknowledgementContinuesPagedProgress(t *testing.T) {
	svc, inbound, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	cfg := owner.config
	before, err := owner.state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(cfg.StateFile), "authority")
	state, err := openAuthorityState(dir)
	if err != nil {
		t.Fatal(err)
	}
	preparations := make(map[string]policyauthority.ResetOperationPreparation, 129)
	for i := 0; i < 129; i++ {
		prepared := startupZeroEffectProgram(t, state, fmt.Sprintf("startup-private-page-%03d", i), "bulk", true)
		preparations[prepared.RequestID] = prepared
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	injected := errors.New("private interruption after first128 completions")
	queries := 0
	const callback = "test:startup-paged-completion"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		process := currentXrayProcess()
		if process != nil && process.IsControlReady() && tx.Statement.Table == "client_policy_sources" {
			queries++
			if queries == 257 {
				tx.AddError(injected)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = svc.RestartXray(true)
	if removeErr := db.Callback().Query().Remove(callback); removeErr != nil {
		t.Fatal(removeErr)
	}
	if !errors.Is(err, injected) || queries != 257 || currentXrayProcess().IsRunning() {
		t.Fatalf("paging missed second page or uncertain startup remained active: %v/%d", err, queries)
	}
	state, err = openAuthorityState(dir)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(map[string]policyauthority.ResetOperationCompletion, 128)
	for key, original := range preparations {
		retained, err := state.Journal.LookupResetPreparation(key)
		if err != nil || retained != original {
			t.Fatalf("interruption changed preparation: %s/%v", key, err)
		}
		done, err := state.Journal.LookupResetCompletion(key)
		if err == nil {
			completed[key] = done
		} else if !errors.Is(err, policyauthority.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if len(completed) != 128 {
		t.Fatalf("durable progress lost: %d", len(completed))
	}
	after, err := state.Journal.Account(client.StableID)
	if err != nil || after != before {
		t.Fatalf("private no-op completion changed account: %v", err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner = managedAuthorityForProcess(currentXrayProcess())
	for key, original := range preparations {
		retained, err := owner.state.Journal.LookupResetPreparation(key)
		if err != nil || retained != original {
			t.Fatalf("continuation changed preparation: %s/%v", key, err)
		}
		done, err := owner.state.Journal.LookupResetCompletion(key)
		if err != nil || done.PreparationDigest != authorityResetSnapshotDigest(original.Snapshot) {
			t.Fatalf("continuation omitted original completion: %s/%v", key, err)
		}
		if previous, ok := completed[key]; ok && previous != done {
			t.Fatalf("continuation rewrote durable completion: %s", key)
		}
	}
	after, err = owner.state.Journal.Account(client.StableID)
	if err != nil || after != before {
		t.Fatalf("continuation changed private accounting: %v", err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "next")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	after, err = owner.state.Journal.Account(client.StableID)
	if err != nil || after.Usage != (policyauthority.Usage{RawUpload: 104, RawDownload: 204, BilledBytes: 316}) || after.Policy != before.Policy {
		t.Fatalf("paging altered subsequent2x billing: %+v/%v", after, err)
	}
}
