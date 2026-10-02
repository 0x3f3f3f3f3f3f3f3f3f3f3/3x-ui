package clientpolicy

import (
	"fmt"
	"sort"
	"time"
)

func (e *Engine) ApplyBatch(policies []Policy) error { return e.applyBatch(policies, true) }

func (e *Engine) applyBatch(policies []Policy, commit bool) error {
	if len(policies) > maxStoredClients {
		return ErrQueueFull
	}
	policies = append([]Policy(nil), policies...)
	sort.Slice(policies, func(i, j int) bool { return policies[i].ClientID < policies[j].ClientID })
	for i, p := range policies {
		if err := p.Validate(); err != nil {
			return err
		}
		if i > 0 && policies[i-1].ClientID == p.ClientID {
			return ErrInvalidPolicy
		}
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrEngineClosed
	}
	if e.failed.Load() {
		e.mu.Unlock()
		return ErrStorage
	}
	clients := make([]*clientState, 0, len(policies))
	unlock := func() {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].mu.Unlock()
		}
		e.mu.Unlock()
	}
	additions := 0
	records := make([]storedClient, 0, len(policies))
	for _, p := range policies {
		c := e.clients[p.ClientID]
		if c == nil {
			additions++
			c = newClientState(e)
		}
		c.mu.Lock()
		clients = append(clients, c)
		if c.revoked {
			unlock()
			return ErrRevoked
		}
		if p.Version < c.policy.Version || p.Version == c.policy.Version && p != c.policy {
			unlock()
			return ErrPolicyVersion
		}
		if _, err := p.effectiveExpiry(c.firstUseForPolicyLocked(p)); err != nil {
			unlock()
			return err
		}
		if _, err := p.quotaUsage(c.usage, c.uncertain); err != nil {
			unlock()
			return err
		}
		if p != c.policy {
			records = append(records, storedClient{AuthorityGrant: c.authorityRecordLocked(0), FirstUsedAt: c.firstUseForPolicyLocked(p), Policy: p, Usage: c.usage, UncertainBytes: c.uncertain, InitializationHash: c.initializationHash})
		}
	}
	if len(e.clients)+additions > maxStoredClients {
		unlock()
		return ErrQueueFull
	}
	if !commit {
		unlock()
		return nil
	}
	var sequences []uint64
	if e.store != nil && len(records) > 0 {
		var err error
		sequences, err = e.store.saveBatch(records)
		if err != nil {
			unlock()
			return e.storageFailed(fmt.Errorf("%w: policy batch: %w", ErrStorage, err))
		}
	}
	var closeList []*Session
	recordIndex := 0
	now := time.Now()
	for i, p := range policies {
		c := clients[i]
		if p == c.policy {
			continue
		}
		if e.store != nil {
			c.sequence = sequences[recordIndex]
		}
		recordIndex++
		c.reservationLeft = 0
		c.checkpointDirty = false
		c.firstUsedAt = c.firstUseForPolicyLocked(p)
		c.policy = p
		e.clients[p.ClientID] = c
		if e.bootID == "" || c.grant == nil {
			c.buckets[Upload].update(p.UploadRate, p.BurstBytes, now)
			c.buckets[Download].update(p.DownloadRate, p.BurstBytes, now)
		}
		c.notifyLocked()
		c.armExpiryLocked()
		if c.reasonsLocked(now) != 0 && !c.transitionPendingLocked(now) {
			closeList = append(closeList, c.sessionsLocked()...)
		}
	}
	unlock()
	closeSessions(closeList)
	return nil
}
