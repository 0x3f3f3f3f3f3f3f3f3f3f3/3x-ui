package service

import (
	"context"
	"errors"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
)

type managedConnectionRetry struct {
	next  time.Time
	delay time.Duration
}

// Snapshot discovery, enrollment and replacement share one context-aware
// owner. Coordinator/SQL locks remain released during TLS peer operations.
func (c *managedPolicyCoordinator) acquireConnection(ctx context.Context) error {
	for !c.connectionMu.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		c.connectionMu.Unlock()
		return err
	}
	return nil
}

// Only the retained product owner starts this worker. Each turn checks one
// configured node, so one unreachable peer cannot consume a whole-node sweep.
func (c *managedPolicyCoordinator) startReconciler() {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	if c.reconcileCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.reconcileCancel, c.reconcileDone = cancel, make(chan struct{})
	go c.runReconciler(ctx, c.reconcileDone)
}

func (c *managedPolicyCoordinator) stopReconciler(ctx context.Context) error {
	c.reconcileMu.Lock()
	cancel, done := c.reconcileCancel, c.reconcileDone
	c.reconcileMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *managedPolicyCoordinator) runReconciler(ctx context.Context, done chan struct{}) {
	defer close(done)
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	cursor := ""
	retries := make(map[string]managedConnectionRetry)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		operation, cancel := context.WithTimeout(ctx, 3*time.Second)
		row, retry, err := c.nextReconcileConnection(operation, &cursor)
		if err == nil && retry {
			state := retries[row.NodeID]
			if !time.Now().Before(state.next) {
				_, err = c.ConnectNode(operation, row.InventoryID, managedAuthorityMember{NodeID: row.NodeID, SourceID: row.SourceID})
				if err == nil {
					state.delay = time.Second
				} else {
					state.delay = min(max(state.delay*2, time.Second), 30*time.Second)
				}
				state.next = time.Now().Add(state.delay)
				retries[row.NodeID] = state
			}
		}
		cancel()
	}
}

func (c *managedPolicyCoordinator) nextReconcileConnection(ctx context.Context, cursor *string) (model.ClientPolicyCoordinatorNode, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var row model.ClientPolicyCoordinatorNode
	var enabled bool
	err := c.withCurrent(ctx, func(tx *gorm.DB) error {
		err := tx.Where("node_id > ?", *cursor).Order("node_id").First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			*cursor = ""
			return nil
		}
		if err != nil {
			return err
		}
		*cursor = row.NodeID
		var node model.Node
		err = tx.First(&node, row.InventoryID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		enabled = node.Enable
		return nil
	})
	if err != nil || row.NodeID == "" {
		return row, false, err
	}
	controller := c.controllers[row.NodeID]
	if !enabled {
		if controller != nil {
			err = controller.Stop(ctx)
			// Its original unsealed allowance remains in the journal even if
			// the disabled peer cannot return a final authentic receipt.
			controller.stateMu.Lock()
			joined := controller.stopped
			controller.stateMu.Unlock()
			if joined {
				delete(c.controllers, row.NodeID)
				delete(c.discovered, row.NodeID)
			}
		}
		return row, false, err
	}
	if controller == nil {
		return row, true, nil
	}
	controller.stateMu.Lock()
	retry := controller.stopped || controller.lastError != nil
	controller.stateMu.Unlock()
	return row, retry, nil
}
