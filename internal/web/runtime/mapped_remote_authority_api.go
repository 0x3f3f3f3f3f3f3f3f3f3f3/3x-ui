package runtime

import (
	"context"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"slices"
)

type MappedRemoteAuthorityAPI struct {
	api    *RemoteAuthorityAPI
	global map[string]policyauthority.ClientMapping
	local  map[string]policyauthority.ClientMapping
}

func NewMappedRemoteAuthorityAPI(api *RemoteAuthorityAPI, mappings []policyauthority.ClientMapping) (*MappedRemoteAuthorityAPI, error) {
	if api == nil || api.remote == nil || api.binding.Validate() != nil || len(mappings) == 0 || len(mappings) > 100000 || !slices.Contains(api.capabilities.GetCapabilities(), "client-authority-history-v1") {
		return nil, ErrNodeAuthorityDiscovery
	}
	pinned := *api
	pinned.capabilities = proto.Clone(api.capabilities).(*command.Capabilities)
	result := &MappedRemoteAuthorityAPI{api: &pinned, global: make(map[string]policyauthority.ClientMapping, len(mappings)), local: make(map[string]policyauthority.ClientMapping, len(mappings))}
	anchor := mappings[0].NodeAnchor
	for _, m := range mappings {
		if m.Validate() != nil || m.NodeAnchor != anchor || m.SourceID != api.binding.ExpectedInstanceID || m.NodeID != api.binding.NodeID || m.Authority.AuthorityID != api.binding.AuthorityID || m.Authority.Generation != api.binding.Generation {
			return nil, ErrNodeAuthorityDiscovery
		}
		if _, exists := result.global[m.GlobalClientID]; exists {
			return nil, ErrNodeAuthorityDiscovery
		}
		if _, exists := result.local[m.LocalClientID]; exists {
			return nil, ErrNodeAuthorityDiscovery
		}
		result.global[m.GlobalClientID], result.local[m.LocalClientID] = m, m
	}
	return result, nil
}
func (a *MappedRemoteAuthorityAPI) checkContext(ctx context.Context) error {
	if a == nil || a.api == nil || ctx == nil {
		return ErrNodeAuthorityDiscovery
	}
	return ctx.Err()
}
func (a *MappedRemoteAuthorityAPI) Capabilities() *command.Capabilities {
	if a == nil || a.api == nil {
		return nil
	}
	return a.api.Capabilities()
}
func (a *MappedRemoteAuthorityAPI) BindAuthority(ctx context.Context, binding *command.AuthorityBinding) error {
	if err := a.checkContext(ctx); err != nil {
		return err
	}
	return a.api.BindAuthority(ctx, binding)
}
func (a *MappedRemoteAuthorityAPI) EnableAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding) error {
	return a.BindAuthority(ctx, binding)
}
func (a *MappedRemoteAuthorityAPI) AuthorityChallenge(ctx context.Context) (*command.AuthorityChallenge, error) {
	if err := a.checkContext(ctx); err != nil {
		return nil, err
	}
	return a.api.AuthorityChallenge(ctx)
}
func (a *MappedRemoteAuthorityAPI) ReadAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding, limit uint32) (*command.AuthorityRequests, error) {
	if err := a.checkContext(ctx); err != nil {
		return nil, err
	}
	if limit == 0 || limit > 128 {
		return nil, ErrNodeAuthorityDiscovery
	}
	page, err := a.api.ReadAuthorityRequests(ctx, binding, 128)
	if err != nil {
		return nil, err
	}
	result := proto.Clone(page).(*command.AuthorityRequests)
	result.Requests = nil
	for _, demand := range page.Requests {
		m, known := a.local[demand.ClientId]
		if !known {
			continue
		}
		if demand.PolicyVersion != m.LocalPolicyVersion {
			return nil, ErrNodeAuthorityDiscovery
		}
		if len(result.Requests) >= int(limit) {
			continue
		}
		copy := proto.Clone(demand).(*command.AuthorityRequest)
		copy.ClientId, copy.PolicyVersion = m.GlobalClientID, m.GlobalPolicyVersion
		result.Requests = append(result.Requests, copy)
	}
	return result, nil
}
func (a *MappedRemoteAuthorityAPI) mapping(ctx context.Context, client string) (policyauthority.ClientMapping, error) {
	if err := a.checkContext(ctx); err != nil {
		return policyauthority.ClientMapping{}, err
	}
	m, known := a.global[client]
	if !known {
		return m, ErrNodeAuthorityDiscovery
	}
	return m, nil
}
func (a *MappedRemoteAuthorityAPI) canonicalState(state *command.ExecutionGrantState, m policyauthority.ClientMapping) (*command.ExecutionGrantState, error) {
	if state == nil || state.Grant == nil || state.Grant.ClientId != m.LocalClientID || state.Grant.PolicyVersion != m.LocalPolicyVersion {
		return nil, ErrNodeAuthorityDiscovery
	}
	copy := proto.Clone(state).(*command.ExecutionGrantState)
	copy.Grant.ClientId, copy.Grant.PolicyVersion = m.GlobalClientID, m.GlobalPolicyVersion
	return copy, nil
}
func (a *MappedRemoteAuthorityAPI) InstallAuthorityGrant(ctx context.Context, grant *command.ExecutionGrant) (*command.ExecutionGrantState, error) {
	if grant == nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	m, err := a.mapping(ctx, grant.ClientId)
	if err != nil {
		return nil, err
	}
	if grant.PolicyVersion != m.GlobalPolicyVersion {
		return nil, ErrNodeAuthorityDiscovery
	}
	copy := proto.Clone(grant).(*command.ExecutionGrant)
	copy.ClientId, copy.PolicyVersion = m.LocalClientID, m.LocalPolicyVersion
	state, err := a.api.InstallAuthorityGrant(ctx, copy)
	if err != nil {
		return nil, err
	}
	return a.canonicalState(state, m)
}
func (a *MappedRemoteAuthorityAPI) grantState(ctx context.Context, client, grant string, operation func(context.Context, string, string) (*command.ExecutionGrantState, error)) (*command.ExecutionGrantState, error) {
	m, err := a.mapping(ctx, client)
	if err != nil {
		return nil, err
	}
	state, err := operation(ctx, m.LocalClientID, grant)
	if err != nil {
		return nil, err
	}
	return a.canonicalState(state, m)
}
func (a *MappedRemoteAuthorityAPI) GetAuthorityGrant(ctx context.Context, client, grant string) (*command.ExecutionGrantState, error) {
	if err := a.checkContext(ctx); err != nil {
		return nil, err
	}
	return a.grantState(ctx, client, grant, a.api.GetAuthorityGrant)
}
func (a *MappedRemoteAuthorityAPI) PauseAuthorityGrant(ctx context.Context, client, grant string) (*command.ExecutionGrantState, error) {
	if err := a.checkContext(ctx); err != nil {
		return nil, err
	}
	return a.grantState(ctx, client, grant, a.api.PauseAuthorityGrant)
}
func (a *MappedRemoteAuthorityAPI) SealAuthorityGrant(ctx context.Context, client, grant string) (*command.ExecutionGrantState, error) {
	if err := a.checkContext(ctx); err != nil {
		return nil, err
	}
	return a.grantState(ctx, client, grant, a.api.SealAuthorityGrant)
}
func (a *MappedRemoteAuthorityAPI) RenewAuthorityGrant(ctx context.Context, renewal *command.AuthorityRenewalRequest) error {
	if renewal == nil {
		return ErrNodeAuthorityDiscovery
	}
	m, err := a.mapping(ctx, renewal.ClientId)
	if err != nil {
		return err
	}
	copy := proto.Clone(renewal).(*command.AuthorityRenewalRequest)
	copy.ClientId = m.LocalClientID
	return a.api.RenewAuthorityGrant(ctx, copy)
}
