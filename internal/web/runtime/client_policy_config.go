package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type ManagedConfigRuntime interface {
	ApplyManagedConfig(context.Context, *xray.Process, *xray.Config, func(*command.Capabilities, *conf.ClientPolicyConfig) (*ManagedPolicyBootstrap, error)) (bool, error)
}

type ManagedChangeRuntime interface {
	ReconcileManagedChange(context.Context) (bool, error)
}

var ErrManagedConfigPartial = errors.New("managed configuration application may be partial")

func (l *Local) ApplyManagedConfig(ctx context.Context, process *xray.Process, next *xray.Config, prepare func(*command.Capabilities, *conf.ClientPolicyConfig) (*ManagedPolicyBootstrap, error)) (applied bool, resultErr error) {
	if process == nil || !process.IsControlReady() || next == nil || prepare == nil {
		return false, errors.New("managed configuration requires a ready core and usage preparation")
	}
	current := process.GetConfig()
	var oldPolicy, newPolicy conf.ClientPolicyConfig
	if err := json.Unmarshal(current.ClientPolicy, &oldPolicy); err != nil {
		return false, err
	}
	if err := json.Unmarshal(next.ClientPolicy, &newPolicy); err != nil {
		return false, err
	}
	compiled, err := newPolicy.Build()
	if err != nil {
		return false, err
	}
	if oldPolicy.InstanceID != newPolicy.InstanceID || oldPolicy.StateFile != newPolicy.StateFile {
		return false, errors.New("managed configuration cannot replace the active accounting store")
	}
	comparable := *next
	comparable.ClientPolicy = current.ClientPolicy
	diff, ok := xray.ComputeManagedHotDiff(current, &comparable)
	if !ok {
		return false, nil
	}
	removedIDs := make([]string, len(diff.RemovedUsers))
	for i, user := range diff.RemovedUsers {
		id, err := managedUserIdentity(current, user)
		if err != nil {
			return false, err
		}
		removedIDs[i] = id
	}
	oldByID := make(map[string]clientpolicy.Policy, len(oldPolicy.Policies))
	for _, p := range oldPolicy.Policies {
		oldByID[p.ClientID] = p
	}
	initial := newPolicy
	initial.Policies = nil
	var changed []*clientpolicy.PolicyConfig
	for i, p := range newPolicy.Policies {
		prior, exists := oldByID[p.ClientID]
		if !exists {
			initial.Policies = append(initial.Policies, p)
		}
		if !exists || prior != p {
			changed = append(changed, compiled.Policies[i])
		}
	}
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		return false, err
	}
	policyAPI, err := xray.DialClientPolicy(ctx, endpoint, newPolicy.InstanceID)
	if err != nil {
		return false, err
	}
	defer policyAPI.Close()
	required, err := xray.ManagedHotDiffCapabilities(diff)
	if err != nil {
		return false, err
	}
	for _, capability := range required {
		if !slices.Contains(policyAPI.Capabilities().Capabilities, capability) {
			return false, fmt.Errorf("%w: missing %s", xray.ErrClientPolicyCapability, capability)
		}
	}
	bootstrap, err := prepare(policyAPI.Capabilities(), &initial)
	if err != nil {
		return false, err
	}
	if err := l.prepareManagedDeletions(ctx, policyAPI, bootstrap, newPolicy.Policies); err != nil {
		return false, errors.Join(ErrManagedConfigPartial, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if process.GetConfig() != current {
		return false, errors.New("managed configuration changed during preparation")
	}
	if err := initializeManagedClients(ctx, policyAPI, bootstrap); err != nil {
		return false, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(ErrManagedConfigPartial, resultErr)
		}
	}()
	var api xray.XrayAPI
	if err := api.InitEndpoint(endpoint); err != nil {
		return false, err
	}
	defer api.Close()
	for i, user := range diff.RemovedUsers {
		if err := api.RemoveUser(user.Tag, user.Email); err != nil && !xray.IsMissingHandlerErr(err) {
			return false, err
		}
		if _, err := policyAPI.CloseInboundConnections(ctx, removedIDs[i], user.Tag); err != nil {
			return false, err
		}
	}
	for _, tag := range diff.RemovedInboundTags {
		if err := api.DelInbound(tag); err != nil && !xray.IsMissingHandlerErr(err) {
			return false, err
		}
	}
	for _, tag := range diff.RemovedOutboundTags {
		if err := api.DelOutbound(tag); err != nil && !xray.IsMissingHandlerErr(err) {
			return false, err
		}
	}
	for start := 0; start < len(changed); start += 1000 {
		if err := policyAPI.Apply(ctx, changed[start:min(start+1000, len(changed))]); err != nil {
			return false, err
		}
	}
	for _, outbound := range diff.AddedOutbounds {
		if err := api.AddOutbound(outbound); err != nil {
			return false, err
		}
	}
	for _, inbound := range diff.AddedInbounds {
		if err := api.AddInbound(inbound); err != nil {
			return false, err
		}
	}
	for _, user := range diff.AddedUsers {
		if err := api.AddUser(user.Protocol, user.Tag, user.User); err != nil {
			return false, err
		}
	}
	if diff.RoutingConfig != nil {
		if err := api.ApplyRoutingConfig(diff.RoutingConfig); err != nil {
			return false, err
		}
	}
	process.SetConfig(next)
	return true, nil
}

func managedUserIdentity(config *xray.Config, user xray.UserOp) (string, error) {
	for _, inbound := range config.InboundConfigs {
		if inbound.Tag != user.Tag {
			continue
		}
		type identity struct {
			Email    string `json:"email"`
			ClientID string `json:"clientId"`
		}
		var accounts struct {
			Clients  []identity `json:"clients"`
			Accounts []identity `json:"accounts"`
			Users    []identity `json:"users"`
		}
		if err := json.Unmarshal(inbound.Settings, &accounts); err != nil {
			return "", err
		}
		entries := accounts.Clients
		if inbound.Protocol == "snell" {
			var owner identity
			if err := json.Unmarshal(inbound.Settings, &owner); err != nil {
				return "", err
			}
			entries = []identity{owner}
		}
		if inbound.Protocol == "mixed" || inbound.Protocol == "http" {
			entries = accounts.Accounts
		}
		if inbound.Protocol == "mieru" || inbound.Protocol == "ssh" {
			entries = accounts.Users
		}
		var id string
		for _, account := range entries {
			if account.Email != user.Email {
				continue
			}
			if account.ClientID == "" || id != "" && id != account.ClientID {
				return "", fmt.Errorf("%w: removed account has conflicting identity", xray.ErrClientPolicyCapability)
			}
			id = account.ClientID
		}
		if id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: removed account has no trusted identity", xray.ErrClientPolicyCapability)
}
