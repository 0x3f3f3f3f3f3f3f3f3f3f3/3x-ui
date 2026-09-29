package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type ManagedDeletionRuntime interface {
	RevokeManagedClients(context.Context, *xray.Process, []string) ([]string, error)
}

func (l *Local) prepareManagedDeletions(ctx context.Context, api *xray.ClientPolicyAPI, bootstrap *ManagedPolicyBootstrap, policies []clientpolicy.Policy) error {
	if bootstrap == nil || len(bootstrap.Initializations) > 100000 {
		return errors.New("invalid managed policy bootstrap")
	}
	l.mu.Lock()
	_, err := api.ReadLedger(ctx, bootstrap.AfterSequence, 1)
	l.mu.Unlock()
	if err != nil {
		return fmt.Errorf("core is behind the panel ledger: %w", err)
	}
	if bootstrap.DeletedClientPage == nil {
		return nil
	}
	active := make(map[string]bool, len(policies)+len(bootstrap.Initializations))
	for _, policy := range policies {
		active[policy.ClientID] = true
	}
	for _, request := range bootstrap.Initializations {
		if request != nil && request.Policy != nil {
			active[request.Policy.ClientId] = true
		}
	}
	for after := ""; ; {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := bootstrap.DeletedClientPage(ctx, after)
		if err != nil || len(ids) == 0 {
			return err
		}
		if len(ids) > 1000 {
			return errors.New("managed deletion page is too large")
		}
		for _, id := range ids {
			if id <= after {
				return errors.New("managed deletion page did not advance")
			}
			if active[id] {
				return clientpolicy.ErrRevoked
			}
			after = id
		}
		l.mu.Lock()
		absent, err := revokeManagedClients(ctx, api, ids)
		l.mu.Unlock()
		if err != nil {
			return err
		}
		if len(absent) > 0 && bootstrap.ConfirmAbsentClients != nil {
			if err := bootstrap.ConfirmAbsentClients(ctx, absent); err != nil {
				return err
			}
		}
	}
}

func (l *Local) RevokeManagedClients(ctx context.Context, process *xray.Process, ids []string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if process == nil || !process.IsControlReady() {
		return nil, errors.New("managed core is not ready for identity deletion")
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return nil, err
	}
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		return nil, err
	}
	api, err := xray.DialClientPolicy(ctx, endpoint, config.InstanceID)
	if err != nil {
		return nil, err
	}
	defer api.Close()
	return revokeManagedClients(ctx, api, ids)
}

func revokeManagedClients(ctx context.Context, api *xray.ClientPolicyAPI, ids []string) ([]string, error) {
	if len(ids) > 1000 {
		return nil, errors.New("identity deletion batch is too large")
	}
	var absent []string
	for _, id := range ids {
		if id == "" {
			return nil, clientpolicy.ErrInvalidPolicy
		}
		state, err := api.GetClient(ctx, id)
		if status.Code(err) == codes.NotFound {
			absent = append(absent, id)
			continue
		}
		if err != nil {
			return nil, err
		}
		if state.Policy == nil {
			return nil, errors.New("managed deletion requires the current policy version")
		}
		if state.Reasons&uint32(clientpolicy.ReasonRevoked) != 0 {
			continue
		}
		if err := api.Revoke(ctx, id, state.Policy.Version); err != nil {
			return nil, err
		}
	}
	return absent, nil
}
