package clientpolicy

import (
	"errors"
	"fmt"
)

// ReconcileUsageFloor raises known cumulative usage before this boot binds an
// authority. It never changes policy, credits reservations or grants payload.
func (e *Engine) ReconcileUsageFloor(expectedBootID, clientID string, floor Usage) error {
	if floor.Remainder >= MultiplierScale {
		return ErrInvalidUsage
	}
	e.mu.Lock()
	err := e.reconcileUsageFloorLocked(expectedBootID, clientID, floor)
	e.mu.Unlock()
	if errors.Is(err, ErrStorage) {
		return e.storageFailed(err)
	}
	return err
}

func (e *Engine) reconcileUsageFloorLocked(expectedBootID, clientID string, floor Usage) error {
	if e.closed {
		return ErrEngineClosed
	}
	if e.failed.Load() || e.store == nil {
		return ErrStorage
	}
	if !e.ready.Load() {
		return ErrEngineNotStarted
	}
	if e.bootID == "" || expectedBootID != e.bootID {
		return ErrAuthority
	}
	e.authorityMu.Lock()
	defer e.authorityMu.Unlock()
	if e.authorityBinding != (AuthorityBinding{}) {
		return ErrAuthority
	}
	c := e.clients[clientID]
	if c == nil {
		return ErrUnknownClient
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revoked {
		return ErrRevoked
	}
	if c.closed {
		return ErrEngineClosed
	}
	if c.grant != nil || len(c.sessions) != 0 || c.pendingOpens != 0 {
		return ErrAuthority
	}
	next := c.usage
	next.RawUpload = max(next.RawUpload, floor.RawUpload)
	next.RawDownload = max(next.RawDownload, floor.RawDownload)
	if billedLess(next, floor) {
		next.BilledBytes, next.Remainder = floor.BilledBytes, floor.Remainder
	}
	if next == c.usage {
		return nil
	}
	if c.policy.ExpiresAt < 0 && c.firstUsedAt == 0 {
		// An older snapshot can lose the real first-use timestamp. Raising its
		// usage without that boundary must not reopen a fresh relative lifetime.
		return ErrAuthority
	}
	if _, err := c.policy.quotaUsage(next, c.uncertain); err != nil {
		return err
	}
	previous := c.previousGrant
	if previous != nil {
		spent, err := grantUsage(c.usage, previous.StartUsage)
		if err != nil {
			return err
		}
		// The floor belongs to known lifetime history, not to this old grant.
		// Shift its counter origin while retaining exactly its prior consumption,
		// reserved capacity and seal; the retired boot gains no execution rights.
		start, err := grantUsage(next, spent)
		if err != nil {
			return err
		}
		copy := *previous
		copy.StartUsage = start
		previous = &copy
	}
	record := storedClient{AuthorityGrant: previous, FirstUsedAt: c.firstUsedAt, Policy: c.policy, Usage: next, UncertainBytes: c.uncertain, ReservedBytes: c.reservationLeft, InitializationHash: c.initializationHash}
	if err := validateStoredAuthorityGrant(record, e.instanceID); err != nil {
		return err
	}
	seq, err := e.store.save(record)
	if err != nil {
		return fmt.Errorf("%w: reconcile known usage: %w", ErrStorage, err)
	}
	c.usage, c.sequence, c.previousGrant = next, seq, previous
	c.notifyLocked()
	return nil
}
