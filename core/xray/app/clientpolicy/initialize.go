package clientpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrAlreadyInitialized = errors.New("client identity already initialized with different state")

// Initialize commits a create-only seed; retries cannot reset usage or reapply the initial policy.
func (e *Engine) Initialize(p Policy, seed Usage) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if seed.Remainder >= MultiplierScale {
		return ErrInvalidUsage
	}
	if _, err := p.quotaUsage(seed, 0); err != nil {
		return err
	}
	encoded, err := json.Marshal(struct {
		Policy Policy
		Usage  Usage
	}{p, seed})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrEngineClosed
	}
	if e.failed.Load() || e.store == nil {
		e.mu.Unlock()
		return ErrStorage
	}
	if existing := e.clients[p.ClientID]; existing != nil {
		existing.mu.Lock()
		defer existing.mu.Unlock()
		defer e.mu.Unlock()
		if existing.revoked {
			return ErrRevoked
		}
		if existing.initializationHash != hash {
			return ErrAlreadyInitialized
		}
		return nil
	}
	if len(e.clients) >= maxStoredClients {
		e.mu.Unlock()
		return ErrQueueFull
	}
	seq, err := e.store.save(storedClient{Policy: p, Usage: seed, InitializationHash: hash})
	if err != nil {
		e.mu.Unlock()
		return e.storageFailed(fmt.Errorf("%w: initialize client: %w", ErrStorage, err))
	}
	c := newClientState(e)
	c.policy, c.usage, c.initializationHash, c.sequence = p, seed, hash, seq
	now := time.Now()
	c.buckets[Upload].update(p.UploadRate, p.BurstBytes, now)
	c.buckets[Download].update(p.DownloadRate, p.BurstBytes, now)
	e.clients[p.ClientID] = c
	if p.ExpiresAt != 0 {
		c.expiry = time.AfterFunc(time.Until(time.UnixMilli(p.ExpiresAt)), c.expire)
	}
	e.mu.Unlock()
	return nil
}
