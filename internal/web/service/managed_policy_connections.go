package service

import (
	"context"
	"errors"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (c *managedPolicyCoordinator) recordConnection(ctx context.Context, inventoryID int, member managedAuthorityMember, api *panelruntime.RemoteAuthorityAPI) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	discovery := c.discovered[member.NodeID]
	if discovery.api != api || discovery.node.Id != inventoryID {
		return ErrManagedConfigStale
	}
	if _, _, _, err := c.enrolledStrategy(member); err != nil {
		return err
	}
	return c.withCurrent(ctx, func(tx *gorm.DB) error {
		var node model.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&node, inventoryID).Error; err != nil {
			return err
		}
		if !node.Enable || !managedNodeConnectionUnchanged(node, discovery.node) {
			return ErrManagedConfigStale
		}
		row := model.ClientPolicyCoordinatorNode{NodeID: member.NodeID, SourceID: member.SourceID, InventoryID: inventoryID}
		var prior model.ClientPolicyCoordinatorNode
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&prior, "node_id = ?", member.NodeID).Error
		if err == nil {
			if prior != row {
				return policyauthority.ErrIdentity
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var count int64
		if err := tx.Model(&model.ClientPolicyCoordinatorNode{}).Count(&count).Error; err != nil {
			return err
		}
		if count >= 1000 {
			return ErrClientPolicyLedger
		}
		return tx.Create(&row).Error
	})
}

// The caller holds c.mu. SQL chooses desired connections, while the original
// mapping proves their source/node identity and the journal bounds all grants.
func (c *managedPolicyCoordinator) configuredConnections(ctx context.Context) ([]model.ClientPolicyCoordinatorNode, error) {
	var rows []model.ClientPolicyCoordinatorNode
	err := c.withCurrent(ctx, func(tx *gorm.DB) error {
		if err := tx.Order("node_id").Limit(1001).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > 1000 {
			return ErrClientPolicyLedger
		}
		return nil
	})
	if err != nil || len(rows) == 0 {
		return rows, err
	}
	original, _, _, err := c.enrolledStrategy(managedAuthorityMember{NodeID: rows[0].NodeID, SourceID: rows[0].SourceID})
	if err != nil {
		return nil, err
	}
	known := make(map[string]string, len(original.members))
	for _, member := range original.members {
		known[member.NodeID] = member.SourceID
	}
	for _, row := range rows {
		if row.InventoryID <= 0 || known[row.NodeID] != row.SourceID {
			return nil, policyauthority.ErrIdentity
		}
	}
	var enabled []model.ClientPolicyCoordinatorNode
	err = c.withCurrent(ctx, func(tx *gorm.DB) error {
		for _, row := range rows {
			var node model.Node
			err := tx.First(&node, row.InventoryID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if node.Enable {
				enabled = append(enabled, row)
			}
		}
		return nil
	})
	return enabled, err
}

func (c *managedPolicyCoordinator) ResumeNodes(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c.mu.Lock()
	rows, err := c.configuredConnections(ctx)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	wanted := make(map[string]bool, len(rows))
	for _, row := range rows {
		wanted[row.NodeID] = true
	}
	var result error
	for nodeID, controller := range c.controllers {
		if !wanted[nodeID] {
			if err := controller.join(ctx); err != nil {
				result = errors.Join(result, err)
				continue
			}
			result = errors.Join(result, controller.Stop(ctx))
			delete(c.controllers, nodeID)
			delete(c.discovered, nodeID)
		}
	}
	c.mu.Unlock()
	for _, row := range rows {
		_, err := c.ConnectNode(ctx, row.InventoryID, managedAuthorityMember{NodeID: row.NodeID, SourceID: row.SourceID})
		result = errors.Join(result, err)
	}
	return result
}
