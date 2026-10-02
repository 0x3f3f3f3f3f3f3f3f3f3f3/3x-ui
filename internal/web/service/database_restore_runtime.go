package service

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"gorm.io/gorm"
)

var databaseRestoreMutex sync.Mutex
var ErrDatabaseRestoreInProgress = errors.New("database restore is already in progress; retry after it finishes")
var errDatabaseRestoreOwnerExpired = errors.New("database restore owner is no longer active")

type databaseRestoreOwner struct{ source *gorm.DB }

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
	return s.xrayService.restartXray(true, owner)
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
