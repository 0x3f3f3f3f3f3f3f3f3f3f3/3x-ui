package runtime

import (
	"context"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"slices"
)

// The adapter snapshots peer configuration and pins one immutable incarnation.
// A replacement boot requires a new coordinator registration and adapter.
type RemoteAuthorityAPI struct {
	remote       *Remote
	binding      NodeAuthorityControlBinding
	capabilities *command.Capabilities
}

func NewRemoteAuthorityAPI(ctx context.Context, remote *Remote, request AuthorityDiscoveryRequest, role NodeExecutionRole) (*RemoteAuthorityAPI, error) {
	if remote == nil || remote.node == nil || role.Validate() != nil || role.Mode != NodeExecutionDelegated {
		return nil, ErrNodeAuthorityDiscovery
	}
	node := *remote.node
	node.InboundTags = slices.Clone(remote.node.InboundTags)
	pinned := NewRemote(&node, remote.egressResolver)
	discovery, err := pinned.DiscoverAuthority(ctx, request)
	if err != nil {
		return nil, err
	}
	if discovery.ExecutionRole == nil || *discovery.ExecutionRole != role {
		return nil, ErrNodeAuthorityDiscovery
	}
	for _, capability := range []string{"on-demand-authority-requests-v1", "monotonic-grant-renewal-v1", "bounded-grant-handoff-v1"} {
		if !slices.Contains(discovery.Capabilities.Capabilities, capability) {
			return nil, ErrNodeAuthorityDiscovery
		}
	}
	return &RemoteAuthorityAPI{remote: pinned, binding: NodeAuthorityControlBinding{ExpectedInstanceID: discovery.Capabilities.InstanceId, ExpectedBootID: discovery.Capabilities.BootId, AuthorityID: role.AuthorityID, Generation: role.Generation, NodeID: role.NodeID}, capabilities: proto.Clone(discovery.Capabilities).(*command.Capabilities)}, nil
}
func (a *RemoteAuthorityAPI) Capabilities() *command.Capabilities {
	if a == nil || a.capabilities == nil {
		return nil
	}
	return proto.Clone(a.capabilities).(*command.Capabilities)
}
func (a *RemoteAuthorityAPI) checkBinding(binding *command.AuthorityBinding) error {
	if a == nil || a.remote == nil || binding == nil || binding.AuthorityId != a.binding.AuthorityID || binding.Generation != a.binding.Generation || binding.NodeId != a.binding.NodeID {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}
func (a *RemoteAuthorityAPI) AuthorityChallenge(ctx context.Context) (*command.AuthorityChallenge, error) {
	if a == nil || a.remote == nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	result, err := a.remote.DiscoverAuthority(ctx, AuthorityDiscoveryRequest{ExpectedInstanceID: a.binding.ExpectedInstanceID, ExpectedBootID: a.binding.ExpectedBootID})
	if err != nil {
		return nil, err
	}
	if result.ExecutionRole == nil || *result.ExecutionRole != a.binding.Role() {
		return nil, ErrNodeAuthorityDiscovery
	}
	return proto.Clone(result.Challenge).(*command.AuthorityChallenge), nil
}
func (a *RemoteAuthorityAPI) BindAuthority(ctx context.Context, binding *command.AuthorityBinding) error {
	if err := a.checkBinding(binding); err != nil {
		return err
	}
	_, err := a.AuthorityChallenge(ctx)
	return err
}
func (a *RemoteAuthorityAPI) EnableAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding) error {
	return a.BindAuthority(ctx, binding)
}
func (a *RemoteAuthorityAPI) ReadAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding, limit uint32) (*command.AuthorityRequests, error) {
	if err := a.checkBinding(binding); err != nil {
		return nil, err
	}
	result, err := a.remote.ReadAuthorityRequests(ctx, NodeAuthorityRequestsRequest{Binding: a.binding, Limit: limit})
	if err != nil {
		return nil, err
	}
	return result.Requests, nil
}
func (a *RemoteAuthorityAPI) InstallAuthorityGrant(ctx context.Context, grant *command.ExecutionGrant) (*command.ExecutionGrantState, error) {
	if a == nil || a.remote == nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	result, err := a.remote.InstallAuthorityGrant(ctx, NodeAuthorityInstallRequest{Binding: a.binding, Grant: grant})
	if err != nil {
		return nil, err
	}
	return result.State, nil
}
func (a *RemoteAuthorityAPI) GetAuthorityGrant(ctx context.Context, client, grant string) (*command.ExecutionGrantState, error) {
	if a == nil || a.remote == nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	result, err := a.remote.GetAuthorityGrant(ctx, NodeAuthorityGrantRequest{Binding: a.binding, ClientID: client, GrantID: grant})
	if err != nil {
		return nil, err
	}
	return result.State, nil
}
func (a *RemoteAuthorityAPI) PauseAuthorityGrant(ctx context.Context, client, grant string) (*command.ExecutionGrantState, error) {
	if a == nil || a.remote == nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	result, err := a.remote.PauseAuthorityGrant(ctx, NodeAuthorityGrantRequest{Binding: a.binding, ClientID: client, GrantID: grant})
	if err != nil {
		return nil, err
	}
	return result.State, nil
}
func (a *RemoteAuthorityAPI) SealAuthorityGrant(ctx context.Context, client, grant string) (*command.ExecutionGrantState, error) {
	if a == nil || a.remote == nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	result, err := a.remote.SealAuthorityGrant(ctx, NodeAuthorityGrantRequest{Binding: a.binding, ClientID: client, GrantID: grant})
	if err != nil {
		return nil, err
	}
	return result.State, nil
}
func (a *RemoteAuthorityAPI) RenewAuthorityGrant(ctx context.Context, renewal *command.AuthorityRenewalRequest) error {
	if a == nil || a.remote == nil {
		return ErrNodeAuthorityDiscovery
	}
	_, err := a.remote.RenewAuthorityGrant(ctx, NodeAuthorityRenewalRequest{Binding: a.binding, Renewal: renewal})
	return err
}
