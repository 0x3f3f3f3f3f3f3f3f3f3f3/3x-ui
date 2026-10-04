package service

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (c *managedPolicyCoordinator) EnrollAccount(ctx context.Context, inventoryID int, parentID string, member managedAuthorityMember, localID string, localVersion uint64) (*panelruntime.NodeClientMappingResult, error) {
	parsed, err := uuid.Parse(localID)
	if c == nil || ctx == nil || inventoryID <= 0 || err != nil || parsed == uuid.Nil || parsed.String() != localID || localVersion == 0 || localVersion > math.MaxInt64 {
		return nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.acquireConnection(ctx); err != nil {
		return nil, err
	}
	defer c.connectionMu.Unlock()
	// Prove the TLS inventory/source/role before creating immutable origins.
	api, err := c.DiscoverNode(ctx, inventoryID, member)
	if err != nil {
		return nil, err
	}
	origin, _, err := c.prepareAccount(ctx, parentID, member, func(tx *gorm.DB) error {
		discovery := c.discovered[member.NodeID]
		if discovery.api != api || discovery.node.Id != inventoryID || api.Capabilities().InstanceId != member.SourceID {
			return ErrManagedConfigStale
		}
		var current model.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, inventoryID).Error; err != nil {
			return err
		}
		if !current.Enable || !managedNodeConnectionUnchanged(discovery.node, current) {
			return ErrManagedConfigStale
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	id, caps := c.state.Journal.Identity(), api.Capabilities()
	request := panelruntime.NodeClientMappingRequest{Binding: panelruntime.NodeAuthorityControlBinding{AuthorityID: id.AuthorityID, Generation: id.Generation, NodeID: member.NodeID, ExpectedInstanceID: member.SourceID, ExpectedBootID: caps.BootId}, GlobalClientID: origin.ClientID, LocalClientID: localID, GlobalPolicyVersion: origin.InitialPolicyVersion, LocalPolicyVersion: localVersion, ExpectedPolicyDigest: origin.PolicyDigest}
	service, err := NewClientPolicyNodeMappingService(c.db, c.state.Journal)
	if err != nil {
		return nil, err
	}
	proof, err := service.Enroll(ctx, api, request)
	if err != nil {
		return nil, err
	}
	if err := c.recordConnection(ctx, inventoryID, member, api); err != nil {
		return nil, err
	}
	return proof, nil
}
