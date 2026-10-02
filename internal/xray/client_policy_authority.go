package xray

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"slices"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
)

func (c *ClientPolicyAPI) EnableAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding) error {
	if err := c.requireAuthorityRequests(ctx, binding); err != nil {
		return err
	}
	_, err := c.client.EnableAuthorityRequests(ctx, &command.AuthorityBindRequest{ExpectedBootId: c.capabilities.BootId, Authority: binding})
	return err
}

func (c *ClientPolicyAPI) ReadAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding, limit uint32) (*command.AuthorityRequests, error) {
	if err := c.requireAuthorityRequests(ctx, binding); err != nil {
		return nil, err
	}
	if limit == 0 || limit > 128 {
		return nil, ErrClientPolicyCapability
	}
	page, err := c.client.ReadAuthorityRequests(ctx, &command.AuthorityRequestsRequest{ExpectedBootId: c.capabilities.BootId, Authority: binding, Limit: limit})
	if err != nil {
		return nil, err
	}
	if page == nil || page.InstanceId != c.capabilities.InstanceId || page.BootId != c.capabilities.BootId || len(page.Requests) > int(limit) {
		return nil, ErrClientPolicyCapability
	}
	clients, requests := make(map[string]bool), make(map[string]bool)
	for _, r := range page.Requests {
		if r == nil || r.ClientId == "" || len(r.ClientId) > 128 || r.PolicyVersion == 0 || r.PolicyVersion > math.MaxInt64 || clients[r.ClientId] || requests[r.RequestId] {
			return nil, ErrClientPolicyCapability
		}
		nonce, err := hex.DecodeString(r.RequestId)
		if err != nil || len(nonce) != 16 {
			return nil, ErrClientPolicyCapability
		}
		clients[r.ClientId], requests[r.RequestId] = true, true
	}
	return page, nil
}

func (c *ClientPolicyAPI) requireAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding) error {
	if ctx == nil || binding == nil || binding.AuthorityId == "" || binding.NodeId == "" || binding.Generation == 0 || binding.Generation > math.MaxInt64 {
		return ErrClientPolicyCapability
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.requireAuthorityCapabilities(); err != nil {
		return err
	}
	if !slices.Contains(c.capabilities.Capabilities, "on-demand-authority-requests-v1") {
		return ErrClientPolicyCapability
	}
	return nil
}

func (c *ClientPolicyAPI) requireAuthorityCapabilities() error {
	if c == nil || c.capabilities == nil || c.capabilities.InstanceId == "" {
		return ErrClientPolicyCapability
	}
	boot, err := hex.DecodeString(c.capabilities.BootId)
	if err != nil || len(boot) != 16 {
		return fmt.Errorf("%w: missing fresh boot identity", ErrClientPolicyCapability)
	}
	for _, feature := range []string{"fresh-core-incarnation-v1", "monotonic-authority-challenge-v1", "boot-bound-execution-grants-v1"} {
		if !slices.Contains(c.capabilities.Capabilities, feature) {
			return fmt.Errorf("%w: missing %s", ErrClientPolicyCapability, feature)
		}
	}
	return nil
}

func (c *ClientPolicyAPI) AuthorityChallenge(ctx context.Context) (*command.AuthorityChallenge, error) {
	if err := c.requireAuthorityCapabilities(); err != nil {
		return nil, err
	}
	return c.client.GetAuthorityChallenge(ctx, &command.AuthorityChallengeRequest{ExpectedBootId: c.capabilities.BootId})
}

func (c *ClientPolicyAPI) BindAuthority(ctx context.Context, binding *command.AuthorityBinding) error {
	if err := c.requireAuthorityCapabilities(); err != nil {
		return err
	}
	if binding == nil {
		return ErrClientPolicyCapability
	}
	_, err := c.client.BindAuthority(ctx, &command.AuthorityBindRequest{ExpectedBootId: c.capabilities.BootId, Authority: binding})
	return err
}

func (c *ClientPolicyAPI) InstallAuthorityGrant(ctx context.Context, grant *command.ExecutionGrant) (*command.ExecutionGrantState, error) {
	if err := c.requireAuthorityCapabilities(); err != nil {
		return nil, err
	}
	if grant == nil || grant.BootId != c.capabilities.BootId || grant.InstanceId != c.capabilities.InstanceId {
		return nil, fmt.Errorf("%w: execution grant identity mismatch", ErrClientPolicyCapability)
	}
	return c.client.InstallAuthorityGrant(ctx, grant)
}

func (c *ClientPolicyAPI) GetAuthorityGrant(ctx context.Context, clientID, grantID string) (*command.ExecutionGrantState, error) {
	if err := c.requireAuthorityCapabilities(); err != nil {
		return nil, err
	}
	if clientID == "" || grantID == "" {
		return nil, ErrClientPolicyCapability
	}
	return c.client.GetAuthorityGrant(ctx, &command.AuthorityGrantRequest{ExpectedBootId: c.capabilities.BootId, ClientId: clientID, GrantId: grantID})
}

func (c *ClientPolicyAPI) SealAuthorityGrant(ctx context.Context, clientID, grantID string) (*command.ExecutionGrantState, error) {
	if err := c.requireAuthorityCapabilities(); err != nil {
		return nil, err
	}
	if clientID == "" || grantID == "" {
		return nil, ErrClientPolicyCapability
	}
	return c.client.SealAuthorityGrant(ctx, &command.AuthorityGrantRequest{ExpectedBootId: c.capabilities.BootId, ClientId: clientID, GrantId: grantID})
}

func (c *ClientPolicyAPI) RenewAuthorityGrant(ctx context.Context, request *command.AuthorityRenewalRequest) error {
	if err := c.requireAuthorityCapabilities(); err != nil {
		return err
	}
	if !slices.Contains(c.capabilities.Capabilities, "monotonic-grant-renewal-v1") || request == nil || request.ExpectedBootId != c.capabilities.BootId {
		return ErrClientPolicyCapability
	}
	_, err := c.client.RenewAuthorityGrant(ctx, request)
	return err
}

func (c *ClientPolicyAPI) PauseAuthorityGrant(ctx context.Context, clientID, grantID string) (*command.ExecutionGrantState, error) {
	if err := c.requireAuthorityCapabilities(); err != nil {
		return nil, err
	}
	if !slices.Contains(c.capabilities.Capabilities, "bounded-grant-handoff-v1") || clientID == "" || grantID == "" {
		return nil, ErrClientPolicyCapability
	}
	return c.client.SealAuthorityGrant(ctx, &command.AuthorityGrantRequest{ExpectedBootId: c.capabilities.BootId, ClientId: clientID, GrantId: grantID, PreserveSessions: true})
}
