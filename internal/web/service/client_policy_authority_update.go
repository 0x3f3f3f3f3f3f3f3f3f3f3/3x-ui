package service

import (
	"context"
	"errors"
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
)

func (a *managedAuthority) PrepareUpdate(ctx context.Context, caps *command.Capabilities, full, initial *conf.ClientPolicyConfig) (*panelruntime.ManagedPolicyBootstrap, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.controller == nil || full == nil || initial == nil || full.InstanceID != a.config.InstanceID || full.StateFile != a.config.StateFile {
		return nil, ErrClientPolicyLedger
	}
	if err := a.reconcileDeletionsLocked(ctx, nil); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(full.Policies))
	for _, policy := range full.Policies {
		account, err := a.state.Journal.LookupAccount(policy.ClientID)
		if errors.Is(err, policyauthority.ErrNotFound) {
			if err := a.provisionPolicy(ctx, policy); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		} else {
			if account.Deleted {
				return nil, policyauthority.ErrDeleted
			}
			if account.Policy.Version != policy.Version {
				if err := a.controller.SuspendClient(ctx, policy.ClientID); err != nil {
					return nil, err
				}
			}
			if err := a.reconcilePolicy(ctx, policy, account); err != nil {
				return nil, err
			}
		}
		ids = append(ids, policy.ClientID)
	}
	bootstrap, err := PrepareLocalClientPolicyBootstrap(caps, initial)
	if err != nil {
		return nil, err
	}
	copy := *full
	copy.Policies = slices.Clone(full.Policies)
	bootstrap.Authorize = func(ctx context.Context, _ *panelxray.ClientPolicyAPI) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closed {
			return ErrClientPolicyLedger
		}
		if err := a.controller.ResumeClients(ids); err != nil {
			return err
		}
		a.config = copy
		return nil
	}
	return bootstrap, nil
}

// Ordinary receipt polling also commits exact grant reports, so restored
// execution counters cannot leave the known lifetime projection behind.
func (a *managedAuthority) Checkpoint(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.controller == nil {
		return ErrClientPolicyLedger
	}
	return a.controller.SettleAndRenew(ctx)
}

func (a *managedAuthority) SuspendClients(ctx context.Context, ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.controller == nil {
		return ErrClientPolicyLedger
	}
	for _, id := range ids {
		if err := a.controller.SuspendClient(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// Caller owns restart serialization. RPCs happen after each SQL transaction has
// completed; suspended identities cannot issue against a partially applied reset.
func (a *managedAuthority) ApplyPolicies(ctx context.Context, managed panelruntime.ManagedProcessRuntime, policies []clientpolicy.Policy) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.controller == nil {
		return ErrClientPolicyLedger
	}
	ids := make([]string, 0, len(policies))
	for _, policy := range policies {
		account, err := a.state.Journal.Account(policy.ClientID)
		if err != nil {
			return err
		}
		if err := a.reconcilePolicy(ctx, policy, account); err != nil {
			return err
		}
		ids = append(ids, policy.ClientID)
	}
	for start := 0; start < len(policies); start += 1000 {
		if err := managed.ApplyManagedPolicies(ctx, a.process, policies[start:min(start+1000, len(policies))]); err != nil {
			return err
		}
	}
	if err := a.controller.ResumeClients(ids); err != nil {
		return err
	}
	byID := make(map[string]clientpolicy.Policy, len(policies))
	for _, policy := range policies {
		byID[policy.ClientID] = policy
	}
	for i, policy := range a.config.Policies {
		if next, ok := byID[policy.ClientID]; ok {
			a.config.Policies[i] = next
		}
	}
	return nil
}
