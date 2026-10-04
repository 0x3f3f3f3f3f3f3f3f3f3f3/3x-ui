package service

import (
	"context"
	"errors"
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"gorm.io/gorm"
)

// SQL selects deletion intent; only the original parent index selects the
// accounts. Tombstoning never releases an outstanding allocation.
func (c *managedPolicyCoordinator) reconcileDeletions(ctx context.Context, parents []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cursor := ""
	for {
		var batch []string
		err := c.withCurrent(ctx, func(tx *gorm.DB) error {
			query := tx.Model(&model.ClientPolicyTombstone{}).Where("client_id > ?", cursor)
			if len(parents) > 0 {
				query = query.Where("client_id IN ?", parents)
			}
			if err := query.Order("client_id").Limit(128).Pluck("client_id", &batch).Error; err != nil {
				return err
			}
			for _, parent := range batch {
				var live int64
				if err := tx.Model(&model.ClientRecord{}).Where("stable_id = ?", parent).Count(&live).Error; err != nil {
					return err
				}
				if live != 0 {
					return ErrClientPolicyLedger
				}
				if err := c.tombstoneParent(tx, parent); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(batch) < 128 {
			break
		}
		cursor = batch[len(batch)-1]
	}
	keys := make([]string, 0, len(c.controllers))
	for key := range c.controllers {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var result error
	for _, key := range keys {
		result = errors.Join(result, c.controllers[key].settleDeletedManagedAccounts(ctx))
	}
	return result
}

func (c *managedPolicyCoordinator) tombstoneParent(tx *gorm.DB, parent string) error {
	cursor := ""
	for {
		page, err := c.state.Journal.ManagedAccountPage(parent, cursor, 128)
		if err != nil {
			return err
		}
		for _, snapshot := range page {
			if err := c.state.Journal.Tombstone(snapshot.Origin.ClientID); err != nil {
				return err
			}
			if err := projectClientPolicyAuthorityTx(tx, c.state.Journal, snapshot.Origin.ClientID); err != nil {
				return err
			}
		}
		if len(page) < 128 || page[len(page)-1].Origin.Scope == "global" {
			return nil
		}
		cursor = page[len(page)-1].Origin.NodeID
	}
}

// Reconcile even a grant whose install acknowledgement was lost. A failed seal
// leaves its original allowance held and available for an authentic retry.
func (c *authorityController) settleDeletedManagedAccounts(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cursor := ""
	var result error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		page, err := c.execution.journal.GrantPage(cursor, 128)
		if err != nil {
			return errors.Join(result, err)
		}
		for _, grant := range page {
			if grant.Sealed || grant.Request.Binding.NodeBoot != c.execution.boot {
				continue
			}
			id := grant.Request.Binding.ClientID
			if _, err := c.execution.journal.ManagedAccountOrigin(id); errors.Is(err, policyauthority.ErrNotFound) {
				continue
			} else if err != nil {
				return errors.Join(result, err)
			}
			account, err := c.execution.journal.Account(id)
			if err != nil {
				return errors.Join(result, err)
			}
			if !account.Deleted {
				continue
			}
			if err := c.execution.Settle(ctx, grant.GrantID, true, false); err != nil {
				result = errors.Join(result, err)
				continue
			}
			if active := c.active[id]; active != nil && active.grantID == grant.GrantID {
				delete(c.active, id)
			}
			delete(c.pending, grant.Request.RequestID)
		}
		if len(page) < 128 {
			return result
		}
		cursor = page[len(page)-1].GrantID
	}
}
