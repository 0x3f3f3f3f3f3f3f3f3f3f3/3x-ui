package clientpolicy

import (
	"context"
	"time"
)

func (e *Engine) PauseAuthorityGrant(clientID, grantID string) (ExecutionGrantState, error) {
	return e.sealAuthorityGrant(clientID, grantID, true)
}

func (c *clientState) transitionPendingLocked(now time.Time) bool {
	return c.grant != nil && c.grant.Sealed && c.grant.preserveSessions && now.Before(c.grant.deadline) && c.reasonsLocked(now) & ^ReasonAuthority == 0
}

// The caller owns mu on entry and return. Paused streams resume on replacement
// or end at the old monotonic lease deadline, cancellation or another restriction.
func (s *Session) awaitGrantTransitionLocked(now time.Time) (bool, error) {
	c := s.client
	if !c.transitionPendingLocked(now) {
		return false, nil
	}
	changed := c.changed
	timer := time.NewTimer(time.Until(c.grant.deadline))
	c.mu.Unlock()
	var err error
	select {
	case <-changed:
	case <-timer.C:
	case <-s.ctx.Done():
		err = ErrSessionClosed
	}
	timer.Stop()
	c.mu.Lock()
	return true, err
}

func (c *clientState) awaitOpenTransitionLocked(ctx context.Context) error {
	if !c.transitionPendingLocked(time.Now()) {
		return nil
	}
	if c.pendingOpens >= maxPendingAdmissions {
		return ErrQueueFull
	}
	c.pendingOpens++
	defer func() { c.pendingOpens-- }()
	for c.transitionPendingLocked(time.Now()) {
		changed := c.changed
		timer := time.NewTimer(time.Until(c.grant.deadline))
		c.mu.Unlock()
		var err error
		select {
		case <-changed:
		case <-timer.C:
		case <-ctx.Done():
			err = ctx.Err()
		}
		timer.Stop()
		c.mu.Lock()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}
