package service

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"gorm.io/gorm"
)

// SQL selects durable deletion intent. A recurring pass visits one parent and
// at most32 original accounts; cursors are disposable and replay is idempotent.
func (c *managedPolicyCoordinator) reconcileDeletions(ctx context.Context, parents []string) error {
	c.mu.Lock()
	var touched []string
	err := c.withCurrent(ctx, func(tx *gorm.DB) error {
		var batch []string
		query := tx.Model(&model.ClientPolicyTombstone{})
		if len(parents) > 0 {
			query = query.Where("client_id IN ?", parents).Order("client_id").Limit(32)
		} else {
			if c.deletionNodeCursor != "" {
				query = query.Where("client_id = ?", c.deletionParentCursor)
			} else {
				query = query.Where("client_id > ?", c.deletionParentCursor)
			}
			query = query.Order("client_id").Limit(1)
		}
		if err := query.Pluck("client_id", &batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 && len(parents) == 0 {
			c.deletionParentCursor, c.deletionNodeCursor = "", ""
		}
		remaining := 32
		for _, parent := range batch {
			if remaining == 0 {
				break
			}
			var live int64
			if err := tx.Model(&model.ClientRecord{}).Where("stable_id = ?", parent).Count(&live).Error; err != nil {
				return err
			}
			if live != 0 {
				return ErrClientPolicyLedger
			}
			cursor := ""
			if len(parents) == 0 {
				cursor = c.deletionNodeCursor
			}
			page, err := c.state.Journal.ManagedAccountPage(parent, cursor, remaining)
			if err != nil {
				return err
			}
			for _, snapshot := range page {
				if !snapshot.Account.Deleted {
					if err := c.state.Journal.Tombstone(snapshot.Origin.ClientID); err != nil {
						return err
					}
				}
				if err := projectClientPolicyAuthorityTx(tx, c.state.Journal, snapshot.Origin.ClientID); err != nil {
					return err
				}
				touched = append(touched, snapshot.Origin.ClientID)
			}
			if len(parents) == 0 {
				c.deletionParentCursor = parent
				c.deletionNodeCursor = ""
				if len(page) == remaining && page[len(page)-1].Origin.Scope == "node" {
					c.deletionNodeCursor = page[len(page)-1].Origin.NodeID
				}
			}
			remaining -= len(page)
		}
		return nil
	})
	keys := make([]string, 0, len(c.controllers))
	for key := range c.controllers {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	selected := make([]*authorityController, 0, 8)
	for _, key := range keys {
		if key > c.deletionControllerCursor && len(selected) < 8 {
			selected = append(selected, c.controllers[key])
			c.deletionControllerCursor = key
		}
	}
	if len(selected) == 0 {
		c.deletionControllerCursor = ""
	}
	// Ordinary explicit deletion selects its live nodes immediately. Periodic
	// work rotates the configured controllers without excluding their renewals.
	if len(parents) > 0 {
		selected = selected[:0]
		for _, key := range keys {
			if len(selected) == 8 {
				break
			}
			selected = append(selected, c.controllers[key])
		}
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	var result error
	for _, controller := range selected {
		result = errors.Join(result, controller.settleDeletedManagedAccounts(ctx, touched...))
	}
	return result
}

// Prioritize live and lost-install grants for the current deletion, then visit
// one bounded historical page. No lifetime scan holds the renewal mutex.
func (c *authorityController) settleDeletedManagedAccounts(ctx context.Context, clients ...string) error {
	c.mu.Lock()
	ids := make(map[string]bool)
	selected := make(map[string]bool)
	for _, id := range clients {
		selected[id] = true
		if active := c.active[id]; active != nil {
			ids[active.grantID] = true
		}
	}
	for _, request := range c.pending {
		if !selected[request.ClientId] {
			continue
		}
		grant, err := c.execution.journal.LookupRequest(c.execution.boot.NodeID, request.ClientId, request.RequestId)
		if err == nil {
			ids[grant.GrantID] = true
		} else if !errors.Is(err, policyauthority.ErrNotFound) {
			c.mu.Unlock()
			return err
		}
		if request.PreviousGrantId != "" {
			ids[request.PreviousGrantId] = true
		}
	}
	page, err := c.execution.journal.GrantPage(c.deletionGrantCursor, 128)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	c.deletionGrantCursor = ""
	if len(page) == 128 {
		c.deletionGrantCursor = page[len(page)-1].GrantID
	}
	for _, grant := range page {
		if !grant.Sealed && grant.Request.Binding.NodeBoot == c.execution.boot {
			ids[grant.GrantID] = true
		}
	}
	c.mu.Unlock()
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	var result error
	seals := 0
	for _, id := range keys {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		c.mu.Lock()
		grant, err := c.execution.journal.Grant(id)
		if err != nil {
			c.mu.Unlock()
			return errors.Join(result, err)
		}
		if grant.Sealed || grant.Request.Binding.NodeBoot != c.execution.boot {
			c.mu.Unlock()
			continue
		}
		client := grant.Request.Binding.ClientID
		if _, err := c.execution.journal.ManagedAccountOrigin(client); errors.Is(err, policyauthority.ErrNotFound) {
			c.mu.Unlock()
			continue
		} else if err != nil {
			c.mu.Unlock()
			return errors.Join(result, err)
		}
		account, err := c.execution.journal.Account(client)
		if err != nil {
			c.mu.Unlock()
			return errors.Join(result, err)
		}
		if !account.Deleted {
			c.mu.Unlock()
			continue
		}
		sealCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		err = c.execution.Settle(sealCtx, id, true, false)
		cancel()
		seals++
		if err == nil {
			if active := c.active[client]; active != nil && active.grantID == id {
				delete(c.active, client)
			}
			delete(c.pending, grant.Request.RequestID)
		}
		c.mu.Unlock()
		result = errors.Join(result, err)
		if seals == 8 {
			break
		}
	}
	return result
}
