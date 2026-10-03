package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func localManagedPolicyRuntime() (runtime.ManagedProcessRuntime, error) {
	manager := runtime.GetManager()
	if manager == nil {
		return nil, errors.New("managed reset requires Runtime")
	}
	rt, err := manager.RuntimeFor(nil)
	if err != nil {
		return nil, err
	}
	managed, ok := rt.(runtime.ManagedProcessRuntime)
	if !ok {
		return nil, errors.New("Runtime does not support managed policies")
	}
	return managed, nil
}

// Restart serialization pins the process; no Runtime mutex is held during SQL work.
func ResetLocalClientPolicy(ctx context.Context, clientID, requestID string) error {
	return ResetLocalClientPolicies(ctx, []string{clientID}, requestID)
}

func ResetLocalClientPolicies(ctx context.Context, clientIDs []string, requestID string) error {
	if !validPolicySourceKey(requestID) {
		return ErrClientPolicyLedger
	}
	ids, err := clientPolicyResetIDs(clientIDs)
	if err != nil {
		return err
	}
	return applyLocalClientPolicyReset(ctx, ids, func(instanceID string) ([]clientpolicy.Policy, error) {
		return PrepareClientPolicyResets(instanceID, ids, requestID)
	})
}

func applyLocalClientPolicyReset(ctx context.Context, ids []string, prepare func(string) ([]clientpolicy.Policy, error)) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	return applyLocalClientPolicyResetLocked(ctx, ids, prepare)
}

// Caller holds the lifecycle lock for the complete enclosing operation.
func applyLocalClientPolicyResetLocked(ctx context.Context, ids []string, prepare func(string) ([]clientpolicy.Policy, error)) (resultErr error) {
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	process := currentXrayProcess()
	if process == nil || !process.IsRunning() || !process.IsControlReady() {
		return errors.New("managed core is not ready for reset")
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return err
	}
	configured := make(map[string]bool, len(config.Policies))
	for _, policy := range config.Policies {
		configured[policy.ClientID] = true
	}
	for _, id := range ids {
		if !configured[id] {
			return clientpolicy.ErrUnknownClient
		}
	}
	managed, err := localManagedPolicyRuntime()
	if err != nil {
		return err
	}
	authority := managedAuthorityForProcess(process)
	if authority != nil {
		defer func() {
			if resultErr != nil {
				stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				resultErr = errors.Join(resultErr, stopManagedProcess(stop, process))
				(&XrayService{}).SetToNeedRestart()
			}
		}()
		if err := authority.SuspendClients(ctx, ids); err != nil {
			return err
		}
	}
	if err := pollLocalClientPolicyLedger(ctx, process); err != nil {
		return fmt.Errorf("checkpoint before reset: %w", err)
	}
	policies, err := prepare(config.InstanceID)
	if err != nil {
		return err
	}
	if authority != nil {
		if err := authority.ApplyPolicies(ctx, managed, policies); err != nil {
			return fmt.Errorf("reset saved but authority application failed: %w", err)
		}
		// Calendar eligibility can omit identities suspended before preparation.
		// Resume the full original set only after every selected policy applied.
		if err := authority.ResumeClients(ctx, ids); err != nil {
			return fmt.Errorf("reset saved but authority resume failed: %w", err)
		}
		return nil
	}
	for start := 0; start < len(policies); start += 1000 {
		if err := managed.ApplyManagedPolicies(ctx, process, policies[start:min(start+1000, len(policies))]); err != nil {
			return fmt.Errorf("reset saved but core application failed: %w", err)
		}
	}
	return nil
}

func reconcileLocalClientPolicies(ctx context.Context, process *xray.Process) (resultErr error) {
	lock.Lock()
	defer lock.Unlock()
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if process != currentXrayProcess() || !process.IsRunning() || !process.IsControlReady() {
		return errors.New("managed core changed during reset reconciliation")
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return err
	}
	managed, err := localManagedPolicyRuntime()
	if err != nil {
		return err
	}
	sourceDB := database.GetDB()
	authority := managedAuthorityForProcess(process)
	if authority != nil {
		sourceDB = authority.db
		defer func() {
			if resultErr != nil {
				stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				resultErr = errors.Join(resultErr, stopManagedProcess(stop, process))
				(&XrayService{}).SetToNeedRestart()
			}
		}()
	}
	changed := false
	for start := 0; start < len(config.Policies); start += 1000 {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := config.Policies[start:min(start+1000, len(config.Policies))]
		pending, err := pendingClientPolicyIDs(sourceDB.WithContext(ctx), config.InstanceID, batch)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			continue
		}
		if authority != nil {
			if err := authority.SuspendClients(ctx, pending); err != nil {
				return err
			}
			if err := pollLocalClientPolicyLedger(ctx, process); err != nil {
				return fmt.Errorf("checkpoint before pending policies: %w", err)
			}
		}
		policies, err := prepareClientPoliciesContextForDatabase(ctx, sourceDB, pending, nil)
		if err != nil {
			return err
		}
		apply := managed.ApplyManagedPolicies
		if authority != nil {
			apply = func(ctx context.Context, _ *xray.Process, policies []clientpolicy.Policy) error {
				return authority.ApplyPolicies(ctx, managed, policies)
			}
		}
		if err := apply(ctx, process, policies); err != nil {
			return fmt.Errorf("retry pending client policies: %w", err)
		}
		changed = true
	}
	if changed {
		return pollLocalClientPolicyLedger(ctx, process)
	}
	return nil
}

func pendingClientPolicyIDs(tx *gorm.DB, instanceID string, policies []clientpolicy.Policy) ([]string, error) {
	ids := make([]string, len(policies))
	for i, policy := range policies {
		ids[i] = policy.ClientID
	}
	resets, err := latestClientPolicyResets(tx, ids)
	if err != nil {
		return nil, err
	}
	var clients []model.ClientRecord
	if err := tx.Select("stable_id", "desired_policy_version").Where("stable_id IN ?", ids).Find(&clients).Error; err != nil {
		return nil, err
	}
	desired := make(map[string]int64, len(clients))
	for _, client := range clients {
		desired[client.StableID] = client.DesiredPolicyVersion
	}
	var receipts []model.ClientPolicyReceipt
	if err := tx.Where("client_id IN ?", ids).Find(&receipts).Error; err != nil {
		return nil, err
	}
	versions := make(map[string]int64, len(receipts))
	revoked := make(map[string]bool, len(receipts))
	for _, receipt := range receipts {
		if receipt.InstanceID != instanceID || versions[receipt.ClientID] != 0 {
			return nil, ErrClientPolicyLedger
		}
		versions[receipt.ClientID] = receipt.PolicyVersion
		revoked[receipt.ClientID] = receipt.Revoked
	}
	var pending []string
	for _, policy := range policies {
		version := desired[policy.ClientID]
		if reset := resets[policy.ClientID]; reset != nil {
			if reset.InstanceID != instanceID || reset.PolicyVersion > version {
				return nil, ErrClientPolicyLedger
			}
		}
		if version == 0 {
			continue
		}
		if version < 0 || versions[policy.ClientID] <= 0 {
			return nil, ErrClientPolicyLedger
		}
		if version > versions[policy.ClientID] || uint64(version) > policy.Version {
			if revoked[policy.ClientID] {
				return nil, ErrClientPolicyLedger
			}
			pending = append(pending, policy.ClientID)
		}
	}
	return pending, nil
}
