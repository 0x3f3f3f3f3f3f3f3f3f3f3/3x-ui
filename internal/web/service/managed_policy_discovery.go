package service

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type managedNodeDiscovery struct {
	node model.Node
	api  *panelruntime.RemoteAuthorityAPI
}

func managedNodeConnectionUnchanged(a, b model.Node) bool {
	return a.Id == b.Id && a.Scheme == b.Scheme && a.Address == b.Address && a.Port == b.Port && a.BasePath == b.BasePath && a.ApiToken == b.ApiToken && a.Enable == b.Enable && a.AllowPrivateAddress == b.AllowPrivateAddress && a.TlsVerifyMode == b.TlsVerifyMode && a.PinnedCertSha256 == b.PinnedCertSha256 && a.OutboundTag == b.OutboundTag && a.Transitive == b.Transitive
}

func (c *managedPolicyCoordinator) DiscoverNode(ctx context.Context, inventoryID int, member managedAuthorityMember) (*panelruntime.RemoteAuthorityAPI, error) {
	if c == nil || ctx == nil || inventoryID <= 0 || !validPolicySourceKey(member.NodeID) || !validPolicySourceKey(member.SourceID) {
		return nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.discovered[member.NodeID]; !exists && len(c.discovered) >= 1000 {
		return nil, ErrClientPolicyLedger
	}
	var node model.Node
	if err := c.withCurrent(ctx, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&node, inventoryID).Error; err != nil {
			return err
		}
		if !node.Enable || node.Transitive || node.Scheme != "" && node.Scheme != "https" {
			return panelruntime.ErrNodeAuthorityDiscovery
		}
		switch node.TlsVerifyMode {
		case "", "verify", "pin", "mtls":
			return nil
		}
		return panelruntime.ErrNodeAuthorityDiscovery
	}); err != nil {
		return nil, err
	}
	remote := panelruntime.NewRemote(&node, panelruntime.GetManager())
	discovery, err := remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil {
		return nil, err
	}
	id := c.state.Journal.Identity()
	role := panelruntime.NodeExecutionRole{Mode: panelruntime.NodeExecutionDelegated, AuthorityID: id.AuthorityID, Generation: id.Generation, NodeID: member.NodeID}
	if discovery.Capabilities.InstanceId != member.SourceID || discovery.ExecutionRole == nil || *discovery.ExecutionRole != role {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	api, err := panelruntime.NewRemoteAuthorityAPI(ctx, remote, panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: member.SourceID, ExpectedBootID: discovery.Capabilities.BootId}, role)
	if err != nil {
		return nil, err
	}
	if err := c.withCurrent(ctx, func(tx *gorm.DB) error {
		var current model.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, inventoryID).Error; err != nil {
			return err
		}
		if !managedNodeConnectionUnchanged(node, current) {
			return ErrManagedConfigStale
		}
		return nil
	}); err != nil {
		return nil, err
	}
	c.discovered[member.NodeID] = managedNodeDiscovery{node: node, api: api}
	return api, nil
}
