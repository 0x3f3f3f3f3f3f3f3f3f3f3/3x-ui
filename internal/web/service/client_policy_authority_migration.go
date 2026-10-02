package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
)

// MigrateLocalClientPolicyAuthority is an explicit stopped-core migration. It
// leaves listeners stopped; subsequent managed startup must obtain fresh grants.
func MigrateLocalClientPolicyAuthority(ctx context.Context, stateDir string) (policyauthority.Identity, error) {
	if ctx == nil || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return policyauthority.Identity{}, ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return policyauthority.Identity{}, err
	}
	owner, err := acquireDatabaseRestore()
	if err != nil {
		return policyauthority.Identity{}, err
	}
	defer owner.release()
	if owner.source == nil {
		return policyauthority.Identity{}, ErrClientPolicyLedger
	}
	if process := currentXrayProcess(); process != nil && len(process.GetConfig().ClientPolicy) == 0 && (process.IsRunning() || process.FinalTrafficPending()) {
		return policyauthority.Identity{}, errors.New("legacy traffic must complete managed handoff before authority migration")
	}
	if err := owner.fenceDatabase(); err != nil {
		return policyauthority.Identity{}, err
	}
	if err := (&ServerService{}).stopCoreForDatabaseRestore(); err != nil {
		return policyauthority.Identity{}, err
	}
	var source model.ClientPolicySource
	if err := owner.source.WithContext(owner.lease.Context(ctx)).Where("node_key = ?", "local").First(&source).Error; err != nil {
		return policyauthority.Identity{}, err
	}
	state, err := migrateAuthorityWithOwner(ctx, owner, filepath.Join(stateDir, "state.db"), source.InstanceID, filepath.Join(stateDir, "authority"))
	if err != nil {
		return policyauthority.Identity{}, err
	}
	id := state.Journal.Identity()
	if err := state.Journal.Close(); err != nil {
		return policyauthority.Identity{}, err
	}
	return id, nil
}

func migrateAuthorityWithOwner(ctx context.Context, owner *databaseRestoreOwner, stateFile, sourceID, dir string) (*durableAuthorityState, error) {
	if ctx == nil {
		return nil, ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if owner == nil || activeDatabaseRestore.Load() != owner || owner.lease == nil {
		return nil, ErrDatabaseRestoreInProgress
	}
	if owner.source != database.GetDB() {
		return nil, database.ErrDatabaseReplaced
	}
	if process := currentXrayProcess(); process != nil && process.IsRunning() {
		return nil, ErrDatabaseRestoreInProgress
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !validPolicySourceKey(sourceID) {
		return nil, ErrAuthorityNotInitialized
	}
	var state *durableAuthorityState
	_, err := os.Lstat(filepath.Join(dir, "journal.db"))
	if errors.Is(err, os.ErrNotExist) {
		manifest, manifestErr := readAuthorityManifest(dir)
		if manifestErr == nil && manifest.Phase == "committed" {
			return nil, ErrAuthorityNotInitialized
		}
		if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
			return nil, manifestErr
		}
		snapshot, err := captureAuthorityMigration(ctx, owner, stateFile, sourceID)
		if err != nil {
			return nil, err
		}
		state, err = initializeAuthorityState(dir, sourceID, snapshot)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		core, err := clientpolicy.InspectPolicyStore(stateFile, sourceID)
		if err != nil {
			return nil, err
		}
		var source model.ClientPolicySource
		if err := owner.source.WithContext(owner.lease.Context(ctx)).Where("node_key = ?", "local").First(&source).Error; err != nil {
			return nil, err
		}
		if source.InstanceID != sourceID || source.Epoch < 0 || source.Sequence < 0 || uint64(source.Epoch) > core.Epoch || uint64(source.Sequence) > core.Sequence {
			return nil, ErrClientPolicyLedger
		}
		if source.HandoffBootID != "" {
			if err := checkLegacyHandoffReceipt(owner.source.WithContext(owner.lease.Context(ctx)), &source); err != nil {
				return nil, err
			}
		}
		state, err = resumeAuthorityState(dir, sourceID)
		if err != nil {
			return nil, err
		}
	}
	err = runSerializedTxContextForDatabase(owner.lease.Context(ctx), owner.source, func(tx *gorm.DB) error {
		var after string
		for {
			accounts, err := state.Journal.AccountPage(after, 1000)
			if err != nil {
				return err
			}
			for _, account := range accounts {
				if err := projectClientPolicyAuthorityTx(tx, state.Journal, account.Seed.ClientID); err != nil {
					return err
				}
			}
			if len(accounts) < 1000 {
				return nil
			}
			after = accounts[len(accounts)-1].Seed.ClientID
		}
	})
	if err != nil {
		_ = state.Journal.Close()
		return nil, err
	}
	return state, nil
}
