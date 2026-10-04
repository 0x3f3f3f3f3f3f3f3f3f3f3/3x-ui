package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

// The caller holds local lifecycle admission before coordinator and SQL locks.
// An existing local original account requires the separately sealed handoff,
// including a zero-use account: it already has an independent original issuer.
func checkManagedOriginalLocalAdmission(ctx context.Context, expected, tx *gorm.DB, ids ...string) error {
	process := currentXrayProcess()
	stateFile := filepath.Join(config.GetDBFolderPath(), "client-policy", "state.db")
	if process != nil && len(process.GetConfig().ClientPolicy) != 0 {
		var policy conf.ClientPolicyConfig
		if json.Unmarshal(process.GetConfig().ClientPolicy, &policy) != nil || !filepath.IsAbs(policy.StateFile) {
			return ErrClientPolicyLedger
		}
		stateFile = policy.StateFile
	}
	dir := filepath.Join(filepath.Dir(stateFile), "authority")
	var state *durableAuthorityState
	if owner := managedAuthorityForProcess(process); owner != nil {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if owner.closed || owner.state == nil || owner.config.InstanceID != owner.state.SourceID || owner.db != expected {
			return ErrClientPolicyLedger
		}
		if err := owner.validateStartupOwner(ctx); err != nil {
			return err
		}
		state = owner.state
	} else {
		_, err := os.Lstat(dir)
		if err == nil {
			if process != nil && process.IsRunning() {
				return ErrAuthorityNotInitialized
			}
			state, err = openAuthorityState(dir)
			if err != nil {
				return err
			}
			defer state.Journal.Close()
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	var source model.ClientPolicySource
	err := tx.Where("node_key = ?", "local").First(&source).Error
	if state != nil {
		if err != nil || source.InstanceID != state.SourceID {
			return ErrAuthorityNotInitialized
		}
		for _, id := range ids {
			if _, err := state.Journal.LookupAccount(id); err == nil {
				return ErrAuthorityNotInitialized
			} else if !errors.Is(err, policyauthority.ErrNotFound) {
				return err
			}
		}
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if _, statErr := os.Lstat(stateFile); errors.Is(statErr, os.ErrNotExist) {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return ErrAuthorityNotInitialized
	} else if statErr != nil {
		return statErr
	}
	if err != nil || process != nil && process.IsRunning() || source.Epoch != 0 || source.Sequence != 0 {
		return ErrAuthorityNotInitialized
	}
	core, err := clientpolicy.InspectPolicyStore(stateFile, source.InstanceID)
	if err != nil || core.Epoch != 0 || core.Sequence != 0 || len(core.Clients) != 0 {
		return ErrAuthorityNotInitialized
	}
	return nil
}
