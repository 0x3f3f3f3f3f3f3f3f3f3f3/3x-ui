package service

import (
	"context"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// Preparation runs while the process has no business listeners; existing committed seeds are immutable.
func PrepareLocalClientPolicyBootstrap(caps *command.Capabilities, config *conf.ClientPolicyConfig) (*runtime.ManagedPolicyBootstrap, error) {
	if caps == nil || config == nil || caps.ApiVersion != 1 || caps.InstanceId != config.InstanceID || len(config.Policies) > 100000 {
		return nil, ErrClientPolicyLedger
	}
	compiled, err := config.Build()
	if err != nil {
		return nil, err
	}
	if err := BindClientPolicySource("local", caps.InstanceId, caps.Epoch); err != nil {
		return nil, err
	}
	after, err := ClientPolicyLedgerCursor(caps.InstanceId)
	if err != nil {
		return nil, err
	}
	bootstrap := &runtime.ManagedPolicyBootstrap{AfterSequence: after, DeletedClientPage: func(ctx context.Context, after string) ([]string, error) {
		return pendingClientPolicyDeletions(ctx, caps.InstanceId, after, false)
	}, ConfirmAbsentClients: func(ctx context.Context, ids []string) error {
		return confirmAbsentClientPolicyDeletions(ctx, caps.InstanceId, ids)
	}}
	for _, policy := range compiled.Policies {
		seed, err := PrepareClientPolicyLedger(caps.InstanceId, policy.ClientId)
		if err != nil {
			return nil, err
		}
		bootstrap.Initializations = append(bootstrap.Initializations, &command.InitializeRequest{Policy: policy, Usage: seed})
	}
	return bootstrap, nil
}
