package service

import (
	"context"
	"strconv"

	"gorm.io/gorm"
)

// ManagedPolicyCoordinatorStatus describes the explicitly activated owner.
// Generations use decimal strings to preserve all integer bits in web clients.
type ManagedPolicyCoordinatorStatus struct {
	Active      bool   `json:"active"`
	AuthorityID string `json:"authorityId"`
	Generation  string `json:"generation"`
}

type ManagedPolicyCoordinatorService struct{}

func (s *ManagedPolicyCoordinatorService) Status(ctx context.Context) (*ManagedPolicyCoordinatorStatus, error) {
	return managedPolicyProductStatus(ctx, false)
}

func (s *ManagedPolicyCoordinatorService) Activate(ctx context.Context) (*ManagedPolicyCoordinatorStatus, error) {
	return managedPolicyProductStatus(ctx, true)
}

func managedPolicyProductStatus(ctx context.Context, activate bool) (*ManagedPolicyCoordinatorStatus, error) {
	c, err := getManagedPolicyCoordinator(ctx, activate)
	if err != nil {
		return nil, err
	}
	status := &ManagedPolicyCoordinatorStatus{}
	if c == nil {
		return status, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	err = c.withCurrent(ctx, func(*gorm.DB) error {
		identity := c.state.Journal.Identity()
		status.Active = true
		status.AuthorityID = identity.AuthorityID
		status.Generation = strconv.FormatUint(identity.Generation, 10)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return status, nil
}
