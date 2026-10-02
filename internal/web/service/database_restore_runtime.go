package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"gorm.io/gorm"
)

var databaseRestoreMutex sync.Mutex
var ErrDatabaseRestoreInProgress = database.ErrRestoreInProgress
var errDatabaseRestoreOwnerExpired = errors.New("database restore owner is no longer active")

type databaseRestoreOwner struct {
	source *gorm.DB
	lease  *database.RestoreLease
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
	defer lock.Unlock()
	if activeDatabaseRestore.Load() == owner {
		activeDatabaseRestore.Store(nil)
		databaseRestoreMutex.Unlock()
	}
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
	return s.xrayService.restartXray(true, owner)
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
	process := currentXrayProcess()
	if process == nil || !process.IsRunning() {
		return nil
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
