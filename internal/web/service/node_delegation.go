package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
)

func (*ClientPolicyNodeService) ConfigureDelegation(ctx context.Context, request panelruntime.NodeDelegationRequest) (*panelruntime.NodeDelegationResult, error) {
	if ctx == nil || request.Validate() != nil {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !lock.TryLock() {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	defer lock.Unlock()
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dir := filepath.Join(config.GetDBFolderPath(), "client-policy", "authority")
	sourceDB := database.GetDB()
	manifest, err := readAuthorityManifest(dir)
	if err == nil && manifest.role() != request.Role() {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	if err == nil && manifest.Phase == "committed" {
		var result *panelruntime.NodeDelegationResult
		err = database.WithCurrentDB(sourceDB, func(current *gorm.DB) error {
			return database.WithConnection(current.WithContext(ctx), func(db *gorm.DB) error {
				var source model.ClientPolicySource
				if err := db.Where("node_key = ?", "local").First(&source).Error; err != nil {
					return err
				}
				if source.InstanceID != manifest.SourceID {
					return policyauthority.ErrIdentity
				}
				if owner := managedAuthorityForProcess(currentXrayProcess()); owner != nil {
					if !owner.mu.TryLock() {
						return panelruntime.ErrNodeAuthorityDiscovery
					}
					defer owner.mu.Unlock()
					if owner.db != sourceDB || owner.state.SourceID != source.InstanceID || owner.state.Role != request.Role() {
						return policyauthority.ErrIdentity
					}
					if err := owner.validateStartupOwner(ctx); err != nil {
						return err
					}
					result = &panelruntime.NodeDelegationResult{InstanceID: source.InstanceID, Role: owner.state.Role}
					return nil
				}
				state, err := openAuthorityState(dir)
				if err != nil {
					return err
				}
				if err := state.Journal.Close(); err != nil {
					return err
				}
				result = &panelruntime.NodeDelegationResult{InstanceID: source.InstanceID, Role: state.Role}
				return nil
			})
		})
		return result, err
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			return nil, ErrAuthorityNotInitialized
		}
	}
	if process := currentXrayProcess(); process != nil && (process.IsRunning() || process.FinalTrafficPending()) {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	stateConfig, err := EnsureLocalClientPolicyState(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	if !databaseRestoreMutex.TryLock() {
		return nil, ErrDatabaseRestoreInProgress
	}
	owner := &databaseRestoreOwner{source: sourceDB}
	if !activeDatabaseRestore.CompareAndSwap(nil, owner) {
		databaseRestoreMutex.Unlock()
		return nil, ErrDatabaseRestoreInProgress
	}
	defer owner.releaseLocked()
	if err := owner.fenceDatabase(); err != nil {
		return nil, err
	}
	core, err := clientpolicy.InspectPolicyStore(stateConfig.StateFile, stateConfig.InstanceID)
	if err != nil {
		return nil, err
	}
	if core.Epoch != 0 || core.Sequence != 0 || len(core.Clients) != 0 {
		return nil, ErrAuthorityNotInitialized
	}
	err = runSerializedTxContextForDatabase(owner.lease.Context(ctx), sourceDB, func(tx *gorm.DB) error {
		var source model.ClientPolicySource
		if err := tx.Where("node_key = ?", "local").First(&source).Error; err != nil {
			return err
		}
		if source.InstanceID != stateConfig.InstanceID || source.Epoch != 0 || source.Sequence != 0 || source.HandoffBootID != "" {
			return ErrAuthorityNotInitialized
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	snapshot, err := captureAuthorityMigration(ctx, owner, stateConfig.StateFile, stateConfig.InstanceID)
	if err != nil {
		return nil, err
	}
	for _, seed := range snapshot.Seeds {
		if seed.Usage != (policyauthority.Usage{}) || seed.WindowUsed != 0 || seed.WindowRemainder != 0 {
			return nil, ErrAuthorityNotInitialized
		}
	}
	state, err := initializeAuthorityStateWithRole(dir, stateConfig.InstanceID, snapshot, request.Role())
	if err != nil {
		return nil, err
	}
	if err := state.Journal.Close(); err != nil {
		return nil, err
	}
	return &panelruntime.NodeDelegationResult{InstanceID: stateConfig.InstanceID, Role: state.Role}, nil
}
