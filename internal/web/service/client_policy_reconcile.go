package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func reconcileLocalClientPolicy(clientID string) error {
	lock.Lock()
	defer lock.Unlock()
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
	policies, err := PrepareClientPolicies([]string{clientID})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := managedRuntime.ApplyManagedPolicies(ctx, process, policies); err != nil {
		return fmt.Errorf("client policy saved but core application failed: %w", err)
	}
	return nil
}
