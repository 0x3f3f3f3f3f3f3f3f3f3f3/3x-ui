package service

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (c *managedPolicyCoordinator) ConnectNode(ctx context.Context, inventoryID int, member managedAuthorityMember) (*authorityController, error) {
	if c == nil || ctx == nil {
		return nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	api, err := c.DiscoverNode(ctx, inventoryID, member)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	discovery := c.discovered[member.NodeID]
	if discovery.api != api {
		return nil, ErrManagedConfigStale
	}
	if err := c.withCurrent(ctx, func(tx *gorm.DB) error {
		var current model.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, inventoryID).Error; err != nil {
			return err
		}
		if !current.Enable || !managedNodeConnectionUnchanged(discovery.node, current) {
			return ErrManagedConfigStale
		}
		return nil
	}); err != nil {
		return nil, err
	}
	strategy, clientID, mappingCount, err := c.enrolledStrategy(member)
	if err != nil {
		return nil, err
	}
	connections, err := c.configuredConnections(ctx)
	if err != nil {
		return nil, err
	}
	members := make([]managedAuthorityMember, 0, len(connections))
	found := false
	for _, connection := range connections {
		current := managedAuthorityMember{NodeID: connection.NodeID, SourceID: connection.SourceID}
		members = append(members, current)
		found = found || current == member && connection.InventoryID == inventoryID
	}
	if !found {
		return nil, ErrManagedConfigStale
	}
	strategy, err = newManagedJournalAllocationStrategy(c.state.Journal, members)
	if err != nil {
		return nil, err
	}
	for _, existing := range c.controllers {
		existing.mu.Lock()
		existing.allocationStrategy = strategy
		existing.mu.Unlock()
	}
	if old := c.controllers[member.NodeID]; old != nil {
		old.stateMu.Lock()
		running := !old.stopped
		old.stateMu.Unlock()
		pinned, ok := old.api.(*managedNodeDemandAPI)
		if running && ok && pinned.mappingCount == mappingCount && managedNodeConnectionUnchanged(pinned.node, discovery.node) && old.execution.boot.SourceID == member.SourceID && old.execution.boot.BootID == api.Capabilities().BootId {
			return old, nil
		}
		if err := old.Stop(ctx); err != nil {
			// Discovery proved a fresh boot under the same original source and
			// delegated role. Its predecessor cannot provide a current receipt;
			// RegisterBoot quarantines old rates and retains its billed capacity.
			if ctx.Err() != nil || old.execution.boot.SourceID != member.SourceID || old.execution.boot.BootID == api.Capabilities().BootId {
				return nil, err
			}
		}
	}
	mappingService, err := NewClientPolicyNodeMappingService(c.db, c.state.Journal)
	if err != nil {
		return nil, err
	}
	mapped, err := mappingService.AuthorityAPI(ctx, api)
	if err != nil {
		return nil, err
	}
	guarded := &managedNodeDemandAPI{authorityDemandAPI: mapped, db: c.db, journal: c.state.Journal, state: c.state, dir: c.dir, node: discovery.node, clientID: clientID, mappingCount: mappingCount}
	controller, err := newAuthorityControllerWithStrategy(ctx, c.db, c.state.Journal, member.NodeID, guarded, strategy)
	if err != nil {
		return nil, err
	}
	// Only an authentic receipt from the original boot releases its allowance.
	// Missing grants and unreachable peers leave the original allocation held.
	if err := recoverManagedNodeGrants(ctx, controller); err != nil {
		return nil, err
	}
	if err := controller.Start(); err != nil {
		return nil, err
	}
	c.controllers[member.NodeID] = controller
	return controller, nil
}

func (c *managedPolicyCoordinator) enrolledStrategy(member managedAuthorityMember) (*managedAllocationStrategy, string, int, error) {
	var members []managedAuthorityMember
	seen := make(map[string]managedAuthorityMember)
	cursor, clientID := "", ""
	mappingCount := 0
	for {
		page, err := c.state.Journal.ClientMappings(policyauthority.ClientMappingCoordinator, cursor, 128)
		if err != nil {
			return nil, "", 0, err
		}
		for _, mapping := range page {
			current := managedAuthorityMember{NodeID: mapping.NodeID, SourceID: mapping.SourceID}
			if prior, exists := seen[current.NodeID]; exists && prior != current {
				return nil, "", 0, policyauthority.ErrIdentity
			} else if !exists {
				if len(members) >= 1000 {
					return nil, "", 0, ErrClientPolicyLedger
				}
				seen[current.NodeID] = current
				members = append(members, current)
			}
			if current == member {
				mappingCount++
				if clientID == "" {
					clientID = mapping.GlobalClientID
				}
			}
		}
		if len(page) < 128 {
			break
		}
		cursor = policyauthority.ClientMappingCursor(page[len(page)-1])
	}
	if clientID == "" {
		return nil, "", 0, policyauthority.ErrNotFound
	}
	strategy, err := newManagedJournalAllocationStrategy(c.state.Journal, members)
	return strategy, clientID, mappingCount, err
}

func recoverManagedNodeGrants(ctx context.Context, controller *authorityController) error {
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := controller.execution.journal.GrantPage(cursor, 128)
		if err != nil {
			return err
		}
		for _, grant := range page {
			if !grant.Sealed && grant.Request.Binding.NodeBoot == controller.execution.boot {
				if err := controller.execution.Settle(ctx, grant.GrantID, true, false); err != nil {
					return err
				}
			}
		}
		if len(page) < 128 {
			return nil
		}
		cursor = page[len(page)-1].GrantID
	}
}
