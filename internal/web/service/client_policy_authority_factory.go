package service

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

type managedAuthority struct {
	mu         sync.Mutex
	process    *panelxray.Process
	config     conf.ClientPolicyConfig
	db         *gorm.DB
	state      *durableAuthorityState
	controller *authorityController
	api        *panelxray.ClientPolicyAPI
	closed     bool
}

func openManagedAuthority(process *panelxray.Process, config *conf.ClientPolicyConfig) (*managedAuthority, error) {
	if process == nil || config == nil {
		return nil, ErrClientPolicyLedger
	}
	if _, err := config.Build(); err != nil {
		return nil, err
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		return nil, err
	}
	if state.SourceID != config.InstanceID {
		_ = state.Journal.Close()
		return nil, policyauthority.ErrIdentity
	}
	copy := *config
	copy.Policies = slices.Clone(config.Policies)
	return &managedAuthority{process: process, config: copy, db: database.GetDB(), state: state}, nil
}

func (a *managedAuthority) Prepare(ctx context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.controller != nil || ctx == nil {
		return nil, ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := runSerializedTxContextForDatabase(ctx, a.db, func(tx *gorm.DB) error { return nil }); err != nil {
		return nil, err
	}
	if err := a.reconcileDeletionsLocked(ctx, nil); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(a.config.Policies))
	for _, policy := range a.config.Policies {
		ids = append(ids, policy.ClientID)
	}
	for start := 0; start < len(ids); start += 1000 {
		end := min(start+1000, len(ids))
		current, err := prepareClientPoliciesForDatabase(a.db, ids[start:end], nil)
		if err != nil {
			return nil, err
		}
		for _, policy := range a.config.Policies[start:end] {
			if !slices.Contains(current, policy) {
				return nil, ErrManagedConfigStale
			}
		}
	}
	for _, policy := range a.config.Policies {
		account, err := a.state.Journal.LookupAccount(policy.ClientID)
		if errors.Is(err, policyauthority.ErrNotFound) {
			if err := a.provisionPolicy(ctx, policy); err != nil {
				return nil, err
			}
			account, err = a.state.Journal.Account(policy.ClientID)
			if err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		if err := a.reconcilePolicy(ctx, policy, account); err != nil {
			return nil, err
		}
		account, err = a.state.Journal.Account(policy.ClientID)
		if err != nil {
			return nil, err
		}
		direction := func(rate uint64) policyauthority.Direction {
			if rate == 0 {
				return policyauthority.Direction{Unlimited: true}
			}
			return policyauthority.Direction{Rate: rate, Burst: policy.BurstBytes}
		}
		if account.Deleted || account.Policy.Version != policy.Version || account.Policy.QuotaBytes != policy.QuotaBytes || account.Policy.QuotaUnlimited != (policy.QuotaBytes == 0) || account.Policy.Upload != direction(policy.UploadRate) || account.Policy.Download != direction(policy.DownloadRate) {
			return nil, ErrClientPolicyLedger
		}
	}
	bootstrap, err := PrepareLocalClientPolicyBootstrap(caps, &a.config)
	if err != nil {
		return nil, err
	}
	bootstrap.Authorize = a.authorize
	return bootstrap, nil
}

func (a *managedAuthority) authorize(ctx context.Context, _ *panelxray.ClientPolicyAPI) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.controller != nil {
		return ErrClientPolicyLedger
	}
	endpoint, err := a.process.GetAPIEndpoint()
	if err != nil {
		return err
	}
	api, err := panelxray.DialClientPolicy(ctx, endpoint, a.config.InstanceID)
	if err != nil {
		return err
	}
	controller, err := newAuthorityController(ctx, a.db, a.state.Journal, "local", api)
	if err == nil {
		err = controller.Start()
	}
	if err != nil {
		return errors.Join(err, api.Close())
	}
	a.api, a.controller = api, controller
	return nil
}

func (a *managedAuthority) Stop(ctx context.Context) error {
	if a == nil || ctx == nil {
		return ErrClientPolicyLedger
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	if a.controller != nil {
		if err := a.controller.Stop(ctx); err != nil {
			return err
		}
	}
	var result error
	if a.api != nil {
		result = a.api.Close()
	}
	result = errors.Join(result, a.state.Journal.Close())
	a.closed = true
	return result
}

// A confirmed stopped core cannot spend its grants. Closing its owner leaves
// every unsealed capacity/rate hold in the journal for conservative recovery.
func (a *managedAuthority) closeStopped(ctx context.Context) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return true, nil
	}
	if a.process == nil || a.process.IsRunning() {
		return false, ErrClientPolicyLedger
	}
	if a.controller != nil {
		if err := a.controller.join(ctx); err != nil {
			return false, err
		}
	}
	var result error
	if a.api != nil {
		result = a.api.Close()
	}
	result = errors.Join(result, a.state.Journal.Close())
	a.closed = true
	return true, result
}
