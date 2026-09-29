package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

func applyLocalClientPolicyReset(ctx context.Context, ids []string, prepare func(string) ([]clientpolicy.Policy, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
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
	if err := pollLocalClientPolicyLedger(ctx, process); err != nil {
		return fmt.Errorf("checkpoint before reset: %w", err)
	}
	policies, err := prepare(config.InstanceID)
	if err != nil {
		return err
	}
	for start := 0; start < len(policies); start += 1000 {
		if err := managed.ApplyManagedPolicies(ctx, process, policies[start:min(start+1000, len(policies))]); err != nil {
			return fmt.Errorf("reset saved but core application failed: %w", err)
		}
	}
	return nil
}

func reconcileLocalClientPolicyResets(ctx context.Context, process *xray.Process) error {
	lock.Lock()
	defer lock.Unlock()
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
	changed := false
	for start := 0; start < len(config.Policies); start += 1000 {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := config.Policies[start:min(start+1000, len(config.Policies))]
		pending, err := pendingClientPolicyResetIDs(database.GetDB().WithContext(ctx), config.InstanceID, batch)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			continue
		}
		policies, err := PrepareClientPolicies(pending)
		if err != nil {
			return err
		}
		if err := managed.ApplyManagedPolicies(ctx, process, policies); err != nil {
			return fmt.Errorf("retry pending resets: %w", err)
		}
		changed = true
	}
	if changed {
		return pollLocalClientPolicyLedger(ctx, process)
	}
	return nil
}

func pendingClientPolicyResetIDs(tx *gorm.DB, instanceID string, policies []clientpolicy.Policy) ([]string, error) {
	ids := make([]string, len(policies))
	for i, policy := range policies {
		ids[i] = policy.ClientID
	}
	resets, err := latestClientPolicyResets(tx, ids)
	if err != nil {
		return nil, err
	}
	if len(resets) == 0 {
		return nil, nil
	}
	ids = ids[:0]
	for id := range resets {
		ids = append(ids, id)
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
		reset := resets[policy.ClientID]
		if reset == nil {
			continue
		}
		if reset.InstanceID != instanceID || versions[policy.ClientID] <= 0 {
			return nil, ErrClientPolicyLedger
		}
		if reset.PolicyVersion > versions[policy.ClientID] || uint64(reset.PolicyVersion) > policy.Version {
			if revoked[policy.ClientID] {
				return nil, ErrClientPolicyLedger
			}
			pending = append(pending, policy.ClientID)
		}
	}
	return pending, nil
}
