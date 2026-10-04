package service

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
)

type ManagedPolicyEnrollmentRequest struct {
	InventoryID        int    `json:"inventoryId"`
	ParentClientID     string `json:"parentClientId"`
	NodeID             string `json:"nodeId"`
	SourceID           string `json:"sourceId"`
	LocalClientID      string `json:"localClientId"`
	LocalPolicyVersion string `json:"localPolicyVersion"`
}

type ManagedPolicyEnrollmentResult struct {
	ClientID           string `json:"clientId"`
	NodeID             string `json:"nodeId"`
	PolicyVersion      string `json:"policyVersion"`
	LocalPolicyVersion string `json:"localPolicyVersion"`
	Connected          bool   `json:"connected"`
}

func (request ManagedPolicyEnrollmentRequest) Validate() error {
	version, err := strconv.ParseUint(request.LocalPolicyVersion, 10, 64)
	if err != nil || version == 0 || version > math.MaxInt64 || strconv.FormatUint(version, 10) != request.LocalPolicyVersion || request.InventoryID <= 0 || !validPolicySourceKey(request.NodeID) || !validPolicySourceKey(request.SourceID) {
		return ErrClientPolicyLedger
	}
	for _, value := range []string{request.ParentClientID, request.LocalClientID} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			return ErrClientPolicyLedger
		}
	}
	return nil
}

func (s *ManagedPolicyCoordinatorService) Enroll(ctx context.Context, request ManagedPolicyEnrollmentRequest) (*ManagedPolicyEnrollmentResult, error) {
	if ctx == nil || request.Validate() != nil {
		return nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := getManagedPolicyCoordinator(ctx, false)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ErrAuthorityNotInitialized
	}
	version, _ := strconv.ParseUint(request.LocalPolicyVersion, 10, 64)
	member := managedAuthorityMember{NodeID: request.NodeID, SourceID: request.SourceID}
	proof, err := c.EnrollAccount(ctx, request.InventoryID, request.ParentClientID, member, request.LocalClientID, version)
	if err != nil {
		return nil, err
	}
	if _, err := c.ConnectNode(ctx, request.InventoryID, member); err != nil {
		return nil, err
	}
	return &ManagedPolicyEnrollmentResult{ClientID: proof.Mapping.GlobalClientID, NodeID: proof.Mapping.NodeID, PolicyVersion: strconv.FormatUint(proof.Mapping.GlobalPolicyVersion, 10), LocalPolicyVersion: strconv.FormatUint(proof.Mapping.LocalPolicyVersion, 10), Connected: true}, nil
}
