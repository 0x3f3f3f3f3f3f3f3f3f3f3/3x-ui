package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"gorm.io/gorm"
)

var databaseRestoreMutex sync.Mutex
var ErrDatabaseRestoreInProgress = database.ErrRestoreInProgress
var errDatabaseRestoreOwnerExpired = errors.New("database restore owner is no longer active")

type databaseRestoreOwner struct {
	source            *gorm.DB
	lease             *database.RestoreLease
	resumeCoordinator atomic.Bool
}

var activeDatabaseRestore atomic.Pointer[databaseRestoreOwner]

func acquireDatabaseRestore() (*databaseRestoreOwner, error) {
	if !databaseRestoreMutex.TryLock() {
		return nil, ErrDatabaseRestoreInProgress
	}
	lock.Lock()
	defer lock.Unlock()
	owner := &databaseRestoreOwner{source: database.GetDB()}
	activeDatabaseRestore.Store(owner)
	return owner, nil
}

func (owner *databaseRestoreOwner) release() {
	owner.resumeDatabase()
	lock.Lock()
	released := owner.releaseLocked()
	lock.Unlock()
	if released && owner.resumeCoordinator.Swap(false) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := ResumeManagedPolicyCoordinator(ctx); err != nil {
			logger.Warning("resume managed policy coordinator after restore failed:", err)
		}
	}
}

func (owner *databaseRestoreOwner) releaseLocked() bool {
	owner.resumeDatabase()
	if activeDatabaseRestore.Load() == owner {
		activeDatabaseRestore.Store(nil)
		databaseRestoreMutex.Unlock()
		return true
	}
	return false
}

func checkDatabaseRestoreRestart(owner *databaseRestoreOwner) error {
	active := activeDatabaseRestore.Load()
	if owner != nil && active != owner {
		return errDatabaseRestoreOwnerExpired
	}
	if owner == nil && active != nil {
		return ErrDatabaseRestoreInProgress
	}
	return nil
}

func (s *ServerService) restartCoreAfterDatabaseRestore(owner *databaseRestoreOwner) error {
	if err := checkDatabaseRestoreRestart(owner); err != nil {
		return err
	}
	owner.resumeDatabase()
	if err := s.xrayService.restartXray(true, owner); err != nil {
		return err
	}
	owner.resumeCoordinator.Store(true)
	return nil
}

func (owner *databaseRestoreOwner) fenceDatabase() error {
	lease, err := database.BeginRestore()
	if err != nil {
		return err
	}
	owner.lease = lease
	return nil
}

func (owner *databaseRestoreOwner) resumeDatabase() {
	if owner.lease != nil {
		owner.lease.Close()
	}
}

func (owner *databaseRestoreOwner) currentDatabase() *gorm.DB {
	return database.GetDB().WithContext(owner.lease.Context(context.Background()))
}

// Restore may proceed only after the tracked core's wait goroutine confirms
// absence. An already absent core is different from a failed termination.
func (s *ServerService) stopCoreForDatabaseRestore() error {
	lock.Lock()
	defer lock.Unlock()
	isManuallyStopped.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if owner := activeDatabaseRestore.Load(); owner != nil && owner.lease != nil {
		ctx = owner.lease.Context(ctx)
	}
	if err := StopManagedPolicyCoordinator(ctx); err != nil {
		return err
	}
	process := currentXrayProcess()
	if process == nil || !process.IsRunning() {
		return nil
	}
	if err := stopManagedAuthority(ctx, process); err != nil {
		return err
	}
	err := process.Stop()
	if !process.IsRunning() {
		return nil
	}
	if err != nil {
		return fmt.Errorf("core remains running after stop: %w", err)
	}
	return fmt.Errorf("core remains running after stop returned success")
}
