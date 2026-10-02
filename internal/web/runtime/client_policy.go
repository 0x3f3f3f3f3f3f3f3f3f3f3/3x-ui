package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type ManagedPolicyBootstrap struct {
	DeletedClientPage    func(context.Context, string) ([]string, error)
	ConfirmAbsentClients func(context.Context, []string) error
	AfterSequence        uint64
	Initializations      []*command.InitializeRequest
	UsageFloors          map[string]*command.Usage
	// Authorize runs after durable initialization and desired policy application,
	// before listeners open and without the Runtime mutex. Its API is temporary.
	Authorize func(context.Context, *xray.ClientPolicyAPI) error
}

type ManagedProcessRuntime interface {
	StartManagedProcess(context.Context, *xray.Process, func(context.Context, *command.Capabilities) (*ManagedPolicyBootstrap, error)) error
	ApplyManagedPolicies(context.Context, *xray.Process, []clientpolicy.Policy) error
	ReadManagedLedger(context.Context, *xray.Process, uint64, bool) (*command.Capabilities, *command.LedgerPage, error)
}

func (l *Local) ReadManagedLedger(ctx context.Context, process *xray.Process, after uint64, checkpoint bool) (*command.Capabilities, *command.LedgerPage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if process == nil || !process.IsControlReady() {
		return nil, nil, errors.New("managed core is not ready")
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return nil, nil, err
	}
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		return nil, nil, err
	}
	api, err := xray.DialClientPolicy(ctx, endpoint, config.InstanceID)
	if err != nil {
		return nil, nil, err
	}
	defer api.Close()
	if checkpoint {
		if _, err := api.ReadLedger(ctx, after, 1); err != nil {
			return nil, nil, err
		}
		if err := api.Checkpoint(ctx); err != nil {
			return nil, nil, err
		}
	}
	page, err := api.ReadLedger(ctx, after, 1000)
	return api.Capabilities(), page, err
}

func (l *Local) ApplyManagedPolicies(ctx context.Context, process *xray.Process, policies []clientpolicy.Policy) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if process == nil || !process.IsControlReady() {
		return errors.New("managed core is not ready")
	}
	if len(policies) == 0 || len(policies) > 1000 {
		return clientpolicy.ErrInvalidPolicy
	}
	current := process.GetConfig()
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(current.ClientPolicy, &config); err != nil {
		return err
	}
	patch := config
	patch.Policies = policies
	compiled, err := patch.Build()
	if err != nil {
		return err
	}
	byID := make(map[string]int, len(config.Policies))
	for i, policy := range config.Policies {
		byID[policy.ClientID] = i
	}
	for _, policy := range policies {
		i, ok := byID[policy.ClientID]
		if !ok {
			return fmt.Errorf("%w: client requires usage preparation before activation", clientpolicy.ErrUnknownClient)
		}
		config.Policies[i] = policy
	}
	next, err := json.Marshal(config)
	if err != nil {
		return err
	}
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		return err
	}
	api, err := xray.DialClientPolicy(ctx, endpoint, config.InstanceID)
	if err != nil {
		return err
	}
	defer api.Close()
	for _, policy := range policies {
		if _, err := api.GetClient(ctx, policy.ClientID); err != nil {
			return err
		}
	}
	if err := api.Apply(ctx, compiled.Policies); err != nil {
		return err
	}
	if !process.CompareAndSetClientPolicy(current.ClientPolicy, next) {
		return errors.New("managed policy configuration changed during application; reconciliation is required")
	}
	return nil
}

func (l *Local) StartManagedProcess(ctx context.Context, process *xray.Process, prepare func(context.Context, *command.Capabilities) (*ManagedPolicyBootstrap, error)) error {
	if prepare == nil {
		return errors.New("managed policy preparation is required")
	}
	var config conf.ClientPolicyConfig
	if process == nil {
		return errors.New("managed process is required")
	}
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return err
	}
	var bootstrap *ManagedPolicyBootstrap
	return process.StartManagedAuthorized(ctx, func(ctx context.Context, api *xray.ClientPolicyAPI) error {
		var err error
		bootstrap, err = prepare(ctx, api.Capabilities())
		if err != nil {
			return err
		}
		if err := l.prepareManagedDeletions(ctx, api, bootstrap, config.Policies); err != nil {
			return err
		}
		l.mu.Lock()
		err = initializeManagedClients(ctx, api, bootstrap)
		l.mu.Unlock()
		if err != nil {
			return err
		}
		return nil
	}, func(ctx context.Context, api *xray.ClientPolicyAPI) error {
		if bootstrap == nil {
			return errors.New("managed authorization requires prepared bootstrap")
		}
		if bootstrap.Authorize != nil {
			return bootstrap.Authorize(ctx, api)
		}
		return nil
	})
}

func initializeManagedClients(ctx context.Context, api *xray.ClientPolicyAPI, bootstrap *ManagedPolicyBootstrap) error {
	if bootstrap == nil || len(bootstrap.Initializations) > 100000 {
		return errors.New("invalid managed policy bootstrap")
	}
	if _, err := api.ReadLedger(ctx, bootstrap.AfterSequence, 1); err != nil {
		return fmt.Errorf("core is behind the panel ledger: %w", err)
	}
	for _, request := range bootstrap.Initializations {
		if request == nil || request.Policy == nil || request.Usage == nil {
			return errors.New("managed policy initialization requires policy and usage")
		}
		current, err := api.GetClient(ctx, request.Policy.ClientId)
		if status.Code(err) == codes.NotFound {
			seed := maxManagedUsage(request.Usage, bootstrap.UsageFloors[request.Policy.ClientId])
			// A missing execution record cannot prove when a consumed relative
			// lifetime began. Do not create a fresh first-use clock from usage alone.
			if request.Policy.ExpiresAt < 0 && (seed.RawUpload != 0 || seed.RawDownload != 0 || seed.BilledBytes != 0 || seed.Remainder != 0) {
				return fmt.Errorf("%w: historical relative expiry requires the original first-use time", clientpolicy.ErrAuthority)
			}
			if err := api.Initialize(ctx, request.Policy, seed); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if floor := bootstrap.UsageFloors[request.Policy.ClientId]; floor != nil && !managedUsageAtLeast(current.Usage, floor) {
			if err := api.ReconcileUsage(ctx, request.Policy.ClientId, floor); err != nil {
				return err
			}
			current, err = api.GetClient(ctx, request.Policy.ClientId)
			if err != nil {
				return err
			}
		}
		if current.Usage == nil || current.Usage.RawUpload < request.Usage.RawUpload || current.Usage.RawDownload < request.Usage.RawDownload || current.Usage.BilledBytes < request.Usage.BilledBytes {
			return errors.New("core usage is behind the historical initialization seed")
		}
	}
	return nil
}

func managedUsageAtLeast(actual, floor *command.Usage) bool {
	return actual != nil && floor != nil && actual.RawUpload >= floor.RawUpload && actual.RawDownload >= floor.RawDownload && (actual.BilledBytes > floor.BilledBytes || actual.BilledBytes == floor.BilledBytes && actual.Remainder >= floor.Remainder)
}

func maxManagedUsage(seed, floor *command.Usage) *command.Usage {
	result := &command.Usage{RawUpload: seed.RawUpload, RawDownload: seed.RawDownload, BilledBytes: seed.BilledBytes, Remainder: seed.Remainder}
	if floor != nil {
		result.RawUpload, result.RawDownload = max(result.RawUpload, floor.RawUpload), max(result.RawDownload, floor.RawDownload)
		if floor.BilledBytes > result.BilledBytes || floor.BilledBytes == result.BilledBytes && floor.Remainder > result.Remainder {
			result.BilledBytes, result.Remainder = floor.BilledBytes, floor.Remainder
		}
	}
	return result
}
