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
	return process.StartManaged(ctx, func(ctx context.Context, api *xray.ClientPolicyAPI) error {
		bootstrap, err := prepare(ctx, api.Capabilities())
		if err != nil {
			return err
		}
		if err := l.prepareManagedDeletions(ctx, api, bootstrap, config.Policies); err != nil {
			return err
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		return initializeManagedClients(ctx, api, bootstrap)
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
			if err := api.Initialize(ctx, request.Policy, request.Usage); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if current.Usage == nil || current.Usage.RawUpload < request.Usage.RawUpload || current.Usage.RawDownload < request.Usage.RawDownload || current.Usage.BilledBytes < request.Usage.BilledBytes {
			return errors.New("core usage is behind the historical initialization seed")
		}
	}
	return nil
}
