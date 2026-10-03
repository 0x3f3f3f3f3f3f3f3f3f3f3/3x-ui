package service

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagedAuthorityRenewalQueuedStopReleasesReopenedJournal(t *testing.T) {
	svc, _, _, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	cfg := owner.config
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	lock.Lock()
	done := make(chan error, 1)
	go func() { done <- resumeAuthorityClientRenewals(ctx, process) }()
	// Observe the actual wait on the lifecycle lock after its owner lookup.
	// No sleep establishes this ordering; the goroutine's blocked stack does.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	queued := false
	for !queued {
		select {
		case <-ctx.Done():
			lock.Unlock()
			t.Fatal("renewal did not queue behind the lifecycle lock")
		case <-ticker.C:
			stack := make([]byte, 256<<10)
			n := runtime.Stack(stack, true)
			for _, goroutine := range strings.Split(string(stack[:n]), "\n\n") {
				if strings.Contains(goroutine, "managedAuthorityForProcess") {
					continue
				}
				if strings.Contains(goroutine, "resumeAuthorityClientRenewals") && strings.Contains(goroutine, "sync.Mutex.Lock") {
					queued = true
					break
				}
				if strings.Contains(goroutine, "resumeAuthorityClientRenewals") && strings.Contains(goroutine, "sync.(*Mutex).Lock") {
					queued = true
					break
				}
			}
		}
	}
	err := stopManagedProcess(ctx, process)
	lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("queued renewal did not refuse a stopped owner: %v", err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
	if err != nil {
		t.Fatalf("queued stopped renewal leaked its reopened journal and blocks restart: %v", err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("stopped renewal prevents actual restart: %v", err)
	}
}
