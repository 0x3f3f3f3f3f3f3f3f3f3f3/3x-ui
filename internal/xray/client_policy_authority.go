package xray

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
)

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
