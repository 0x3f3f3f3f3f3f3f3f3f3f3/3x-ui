package service

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func (c *managedPolicyCoordinator) EnrollAccount(ctx context.Context, inventoryID int, parentID string, member managedAuthorityMember, localID string, localVersion uint64) (*panelruntime.NodeClientMappingResult, error) {
	parsed, err := uuid.Parse(localID)
	if c == nil || ctx == nil || inventoryID <= 0 || err != nil || parsed == uuid.Nil || parsed.String() != localID || localVersion == 0 || localVersion > math.MaxInt64 {
		return nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	origin, _, err := c.PrepareAccount(ctx, parentID, member)
	if err != nil {
		return nil, err
	}
	api, err := c.DiscoverNode(ctx, inventoryID, member)
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
