package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func reconcileLocalClientPolicy(clientID string) (resultErr error) {
	lock.Lock()
	defer lock.Unlock()
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return err
	}
	process := currentXrayProcess()
	if process == nil || !process.IsRunning() || len(process.GetConfig().ClientPolicy) == 0 {
		return nil
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return err
	}
	managed := false
	for _, policy := range config.Policies {
		if policy.ClientID == clientID {
			managed = true
			break
		}
	}
	if !managed {
		return nil
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
	}
	manager := runtime.GetManager()
	if manager == nil {
		return fmt.Errorf("client policy saved but Runtime is unavailable")
	}
	rt, err := manager.RuntimeFor(nil)
	if err != nil {
		return err
	}
	managedRuntime, ok := rt.(runtime.ManagedProcessRuntime)
	if !ok {
		return fmt.Errorf("client policy saved but Runtime does not support managed policies")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if authority != nil {
		ids := []string{clientID}
		if err := authority.SuspendClients(ctx, ids); err != nil {
			return err
		}
		policies, err := prepareClientPoliciesContextForDatabase(ctx, authority.db, ids, nil)
		if err != nil {
			return err
		}
		if err := authority.ApplyPolicies(ctx, managedRuntime, policies); err != nil {
			return fmt.Errorf("client policy saved but authority application failed: %w", err)
		}
		return nil
	}
	policies, err := PrepareClientPolicies([]string{clientID})
	if err != nil {
		return err
	}
	if err := managedRuntime.ApplyManagedPolicies(ctx, process, policies); err != nil {
		return fmt.Errorf("client policy saved but core application failed: %w", err)
	}
	return nil
}
