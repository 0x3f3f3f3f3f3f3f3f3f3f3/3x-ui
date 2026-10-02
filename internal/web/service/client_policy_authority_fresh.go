package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

// The caller owns the service lifecycle lock. Only an execution source that
// has never booted may establish its first authority baseline automatically.
func initializeFreshAuthorityLocked(ctx context.Context, config *conf.ClientPolicyConfig) error {
	if ctx == nil || config == nil {
		return ErrClientPolicyLedger
	}
	dir := filepath.Join(filepath.Dir(config.StateFile), "authority")
	if _, err := os.Lstat(dir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !databaseRestoreMutex.TryLock() {
		return ErrDatabaseRestoreInProgress
	}
	owner := &databaseRestoreOwner{source: database.GetDB()}
	if !activeDatabaseRestore.CompareAndSwap(nil, owner) {
		databaseRestoreMutex.Unlock()
		return ErrDatabaseRestoreInProgress
	}
	defer owner.releaseLocked()
	if err := owner.fenceDatabase(); err != nil {
		return err
	}
	core, err := clientpolicy.InspectPolicyStore(config.StateFile, config.InstanceID)
	if err != nil {
		return err
	}
	if core.Epoch != 0 || core.Sequence != 0 || len(core.Clients) != 0 {
		return ErrAuthorityNotInitialized
	}
	err = runSerializedTxContextForDatabase(owner.lease.Context(ctx), owner.source, func(tx *gorm.DB) error {
		var source model.ClientPolicySource
		if err := tx.Where("node_key = ?", "local").First(&source).Error; err != nil {
			return err
		}
		if source.InstanceID != config.InstanceID || source.Epoch != 0 || source.Sequence != 0 || source.HandoffBootID != "" {
			return ErrAuthorityNotInitialized
		}
		return nil
	})
	if err != nil {
		return err
	}
	state, err := migrateAuthorityWithOwner(ctx, owner, config.StateFile, config.InstanceID, dir)
	if err != nil {
		return err
	}
	return state.Journal.Close()
}
