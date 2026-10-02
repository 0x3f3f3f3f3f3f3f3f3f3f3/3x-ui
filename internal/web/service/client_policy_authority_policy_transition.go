package service

import (
	"context"
	"errors"
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func (c *authorityController) SuspendClient(ctx context.Context, client string) error {
	return c.suspendClient(ctx, client, true)
}

func (c *authorityController) suspendClient(ctx context.Context, client string, preserve bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkNotStopped(); err != nil {
		return err
	}
	c.suspended[client] = true
	grants := make(map[string]bool)
	if active := c.active[client]; active != nil {
		grants[active.grantID] = true
	}
	for _, request := range c.pending {
		if request.ClientId != client {
			continue
		}
		grant, err := c.execution.journal.LookupRequest(c.execution.boot.NodeID, client, request.RequestId)
		if errors.Is(err, policyauthority.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if grant.Request.Binding.NodeBoot != c.execution.boot {
			return policyauthority.ErrIncarnation
		}
		grants[grant.GrantID] = true
	}
	ids := make([]string, 0, len(grants))
	for id := range grants {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := c.execution.Settle(ctx, id, true, preserve); err != nil {
			return err
		}
	}
	delete(c.active, client)
	for id, request := range c.pending {
		if request.ClientId == client {
			delete(c.pending, id)
		}
	}
	return nil
}

func (c *authorityController) ResumeClients(ids []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkNotStopped(); err != nil {
		return err
	}
	for _, id := range ids {
		delete(c.suspended, id)
	}
	return nil
}
