package service

import (
	"context"
	"errors"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// Keep lifecycle, SQL connection admission and the owned API across one bounded
// operation. A lost reply is an error even if the core committed the mutation.
func withOwnedNodeAuthority(ctx context.Context, binding panelruntime.NodeAuthorityControlBinding, operation func(context.Context, *managedAuthority, panelruntime.NodeAuthorityControlIdentity) error) error {
	if ctx == nil || binding.Validate() != nil {
		return panelruntime.ErrNodeAuthorityDiscovery
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !lock.TryLock() {
		return panelruntime.ErrNodeAuthorityDiscovery
	}
	defer lock.Unlock()
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return err
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	if owner == nil || !owner.mu.TryLock() {
		return panelruntime.ErrNodeAuthorityDiscovery
	}
	defer owner.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return database.WithCurrentDB(owner.db, func(current *gorm.DB) error {
		return database.WithConnection(current.WithContext(ctx), func(_ *gorm.DB) error {
			if err := owner.validateStartupOwner(ctx); err != nil {
				return err
			}
			if !owner.delegated() || owner.state.Role != binding.Role() {
				return panelruntime.ErrNodeAuthorityDiscovery
			}
			caps := owner.api.Capabilities()
			identity := panelruntime.NodeAuthorityControlIdentity{InstanceID: caps.InstanceId, BootID: caps.BootId, ExecutionRole: owner.state.Role}
			if err := identity.Validate(binding); err != nil {
				return err
			}
			err := operation(ctx, owner, identity)
			return errors.Join(err, owner.validateStartupOwner(ctx))
		})
	})
}

func (*ClientPolicyNodeService) ReadAuthorityRequests(ctx context.Context, request panelruntime.NodeAuthorityRequestsRequest) (*panelruntime.NodeAuthorityRequestsResult, error) {
	if request.Validate() != nil {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	var result *panelruntime.NodeAuthorityRequestsResult
	err := withOwnedNodeAuthority(ctx, request.Binding, func(ctx context.Context, owner *managedAuthority, identity panelruntime.NodeAuthorityControlIdentity) error {
		page, err := owner.api.ReadAuthorityRequests(ctx, owner.delegatedBinding(), request.Limit)
		if err != nil {
			return err
		}
		result = &panelruntime.NodeAuthorityRequestsResult{NodeAuthorityControlIdentity: identity}
		if page != nil {
			result.Requests = proto.Clone(page).(*command.AuthorityRequests)
		}
		return result.Validate(request)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (*ClientPolicyNodeService) InstallAuthorityGrant(ctx context.Context, request panelruntime.NodeAuthorityInstallRequest) (*panelruntime.NodeAuthorityGrantResult, error) {
	if request.Grant != nil {
		request.Grant = proto.Clone(request.Grant).(*command.ExecutionGrant)
	}
	if request.Validate() != nil {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	var result *panelruntime.NodeAuthorityGrantResult
	err := withOwnedNodeAuthority(ctx, request.Binding, func(ctx context.Context, owner *managedAuthority, identity panelruntime.NodeAuthorityControlIdentity) error {
		state, err := owner.api.InstallAuthorityGrant(ctx, request.Grant)
		if err != nil {
			return err
		}
		result = &panelruntime.NodeAuthorityGrantResult{NodeAuthorityControlIdentity: identity}
		if state != nil {
			result.State = proto.Clone(state).(*command.ExecutionGrantState)
		}
		if err := result.Validate(panelruntime.NodeAuthorityGrantRequest{Binding: request.Binding, ClientID: request.Grant.ClientId, GrantID: request.Grant.GrantId}); err != nil {
			return err
		}
		if result.State.Sealed || !proto.Equal(result.State.Grant, request.Grant) {
			return panelruntime.ErrNodeAuthorityDiscovery
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func ownedNodeAuthorityGrant(ctx context.Context, request panelruntime.NodeAuthorityGrantRequest, seal bool, operation func(context.Context, *managedAuthority) (*command.ExecutionGrantState, error)) (*panelruntime.NodeAuthorityGrantResult, error) {
	if request.Validate() != nil {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	var result *panelruntime.NodeAuthorityGrantResult
	err := withOwnedNodeAuthority(ctx, request.Binding, func(ctx context.Context, owner *managedAuthority, identity panelruntime.NodeAuthorityControlIdentity) error {
		state, err := operation(ctx, owner)
		if err != nil {
			return err
		}
		result = &panelruntime.NodeAuthorityGrantResult{NodeAuthorityControlIdentity: identity}
		if state != nil {
			result.State = proto.Clone(state).(*command.ExecutionGrantState)
		}
		if err := result.Validate(request); err != nil {
			return err
		}
		if seal && !result.State.Sealed {
			return panelruntime.ErrNodeAuthorityDiscovery
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (*ClientPolicyNodeService) GetAuthorityGrant(ctx context.Context, request panelruntime.NodeAuthorityGrantRequest) (*panelruntime.NodeAuthorityGrantResult, error) {
	return ownedNodeAuthorityGrant(ctx, request, false, func(ctx context.Context, owner *managedAuthority) (*command.ExecutionGrantState, error) {
		return owner.api.GetAuthorityGrant(ctx, request.ClientID, request.GrantID)
	})
}

func (*ClientPolicyNodeService) PauseAuthorityGrant(ctx context.Context, request panelruntime.NodeAuthorityGrantRequest) (*panelruntime.NodeAuthorityGrantResult, error) {
	return ownedNodeAuthorityGrant(ctx, request, true, func(ctx context.Context, owner *managedAuthority) (*command.ExecutionGrantState, error) {
		return owner.api.PauseAuthorityGrant(ctx, request.ClientID, request.GrantID)
	})
}

func (*ClientPolicyNodeService) SealAuthorityGrant(ctx context.Context, request panelruntime.NodeAuthorityGrantRequest) (*panelruntime.NodeAuthorityGrantResult, error) {
	return ownedNodeAuthorityGrant(ctx, request, true, func(ctx context.Context, owner *managedAuthority) (*command.ExecutionGrantState, error) {
		return owner.api.SealAuthorityGrant(ctx, request.ClientID, request.GrantID)
	})
}

func (*ClientPolicyNodeService) RenewAuthorityGrant(ctx context.Context, request panelruntime.NodeAuthorityRenewalRequest) (*panelruntime.NodeAuthorityRenewalResult, error) {
	if request.Renewal != nil {
		request.Renewal = proto.Clone(request.Renewal).(*command.AuthorityRenewalRequest)
	}
	if request.Validate() != nil {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	var result *panelruntime.NodeAuthorityRenewalResult
	err := withOwnedNodeAuthority(ctx, request.Binding, func(ctx context.Context, owner *managedAuthority, identity panelruntime.NodeAuthorityControlIdentity) error {
		if err := owner.api.RenewAuthorityGrant(ctx, request.Renewal); err != nil {
			return err
		}
		result = &panelruntime.NodeAuthorityRenewalResult{NodeAuthorityControlIdentity: identity, Renewal: proto.Clone(request.Renewal).(*command.AuthorityRenewalRequest)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
