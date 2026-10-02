package clientpolicy

import (
	"fmt"
	"math"
	"time"
)

type AuthorityBinding struct {
	AuthorityID string `json:"authorityId"`
	Generation  uint64 `json:"generation"`
	NodeID      string `json:"nodeId"`
}

type AuthorityShare struct {
	Unlimited bool   `json:"unlimited"`
	Rate      uint64 `json:"rate"`
	Burst     uint64 `json:"burst"`
}

type ExecutionGrant struct {
	Authority     AuthorityBinding `json:"authority"`
	InstanceID    string           `json:"instanceId"`
	BootID        string           `json:"bootId"`
	ClientID      string           `json:"clientId"`
	WindowID      string           `json:"windowId"`
	PolicyVersion uint64           `json:"policyVersion"`
	GrantID       string           `json:"grantId"`
	Sequence      uint64           `json:"sequence"`
	ChallengeID   string           `json:"challengeId"`
	Capacity      uint64           `json:"capacity"`
	Upload        AuthorityShare   `json:"upload"`
	Download      AuthorityShare   `json:"download"`
	LeaseDuration time.Duration    `json:"leaseDuration"`
}

type ExecutionGrantState struct {
	Grant    ExecutionGrant
	Usage    Usage
	Sequence uint64
	Sealed   bool
	Deadline time.Time
}

type storedAuthorityGrant struct {
	Grant               ExecutionGrant `json:"grant"`
	StartUsage          Usage          `json:"startUsage"`
	ReservedBilledBytes uint64         `json:"reservedBilledBytes"`
	ReservedRemainder   uint64         `json:"reservedRemainder"`
	Sealed              bool           `json:"sealed"`
}

type runtimeAuthorityGrant struct {
	storedAuthorityGrant
	deadline time.Time
}

func validAuthorityBinding(binding AuthorityBinding) bool {
	return validInstanceID(binding.AuthorityID) && validInstanceID(binding.NodeID) && binding.Generation > 0 && binding.Generation <= math.MaxInt64
}

func validAuthorityShare(share AuthorityShare) bool {
	if share.Unlimited {
		return share.Rate == 0 && share.Burst == 0
	}
	return share.Rate <= 1<<40 && share.Burst <= 1<<20 && ((share.Rate == 0 && share.Burst == 0) || (share.Rate > 0 && share.Burst > 0))
}

func validExecutionGrant(grant ExecutionGrant) bool {
	return validAuthorityBinding(grant.Authority) && validInstanceID(grant.InstanceID) && validInstanceID(grant.BootID) && validInstanceID(grant.ClientID) && validInstanceID(grant.WindowID) && validInstanceID(grant.GrantID) && validInstanceID(grant.ChallengeID) && grant.PolicyVersion > 0 && grant.PolicyVersion <= math.MaxInt64 && grant.Sequence > 0 && grant.Sequence <= math.MaxInt64 && grant.Capacity > 0 && grant.Capacity <= math.MaxInt64 && validAuthorityShare(grant.Upload) && validAuthorityShare(grant.Download) && grant.LeaseDuration > 0 && grant.LeaseDuration <= MaxAuthorityLeaseDuration
}

func grantUsage(after, before Usage) (Usage, error) {
	if after.RawUpload < before.RawUpload || after.RawDownload < before.RawDownload || after.Remainder >= MultiplierScale || before.Remainder >= MultiplierScale || after.BilledBytes < before.BilledBytes || after.BilledBytes == before.BilledBytes && after.Remainder < before.Remainder {
		return Usage{}, ErrAuthority
	}
	whole, fraction := after.BilledBytes-before.BilledBytes, after.Remainder
	if fraction < before.Remainder {
		whole--
		fraction += MultiplierScale
	}
	return Usage{RawUpload: after.RawUpload - before.RawUpload, RawDownload: after.RawDownload - before.RawDownload, BilledBytes: whole, Remainder: fraction - before.Remainder}, nil
}

func billedLess(a, b Usage) bool {
	return a.BilledBytes < b.BilledBytes || a.BilledBytes == b.BilledBytes && a.Remainder < b.Remainder
}

func (c *clientState) withinAuthorityGrantLocked(usage Usage) bool {
	if c.engine.bootID == "" {
		return true
	}
	if c.grant == nil || c.grant.Sealed {
		return false
	}
	spent, err := grantUsage(usage, c.grant.StartUsage)
	return err == nil && !billedLess(Usage{BilledBytes: c.grant.Grant.Capacity}, spent)
}

func (c *clientState) authorityRecordLocked(reserved uint64) *storedAuthorityGrant {
	if c.grant == nil {
		return c.previousGrant
	}
	record := c.grant.storedAuthorityGrant
	if reserved == 0 {
		spent, _ := grantUsage(c.usage, record.StartUsage)
		record.ReservedBilledBytes, record.ReservedRemainder = spent.BilledBytes, spent.Remainder
	}
	return &record
}

func (c *clientState) authorityStateLocked() (ExecutionGrantState, error) {
	if c.grant == nil || c.grant.Grant.BootID != c.engine.bootID {
		return ExecutionGrantState{}, ErrAuthority
	}
	spent, err := grantUsage(c.usage, c.grant.StartUsage)
	if err != nil {
		return ExecutionGrantState{}, err
	}
	return ExecutionGrantState{Grant: c.grant.Grant, Usage: spent, Sequence: c.sequence, Sealed: c.grant.Sealed, Deadline: c.grant.deadline}, nil
}

func (e *Engine) BindAuthority(expectedBootID string, binding AuthorityBinding) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrEngineClosed
	}
	if e.failed.Load() {
		return ErrStorage
	}
	if !e.ready.Load() {
		return ErrEngineNotStarted
	}
	if e.bootID == "" || expectedBootID != e.bootID || !validAuthorityBinding(binding) {
		return ErrAuthority
	}
	e.authorityMu.Lock()
	defer e.authorityMu.Unlock()
	if e.authorityBinding != (AuthorityBinding{}) && e.authorityBinding != binding {
		return ErrAuthority
	}
	e.authorityBinding = binding
	return nil
}

func (e *Engine) InstallAuthorityGrant(grant ExecutionGrant) (ExecutionGrantState, error) {
	if !validExecutionGrant(grant) || grant.BootID != e.bootID || grant.InstanceID != e.instanceID {
		return ExecutionGrantState{}, ErrAuthority
	}
	e.authorityMu.Lock()
	binding := e.authorityBinding
	e.authorityMu.Unlock()
	if grant.Authority != binding {
		return ExecutionGrantState{}, ErrAuthority
	}
	deadline, err := e.authorityDeadline(grant.ChallengeID, grant.LeaseDuration)
	if err != nil {
		return ExecutionGrantState{}, err
	}
	c, err := e.state(grant.ClientID)
	if err != nil {
		return ExecutionGrantState{}, err
	}
	c.mu.Lock()
	if e.failed.Load() || c.closed {
		c.mu.Unlock()
		return ExecutionGrantState{}, ErrStorage
	}
	if c.grant != nil && c.grant.Grant.GrantID == grant.GrantID {
		if c.grant.Grant != grant {
			c.mu.Unlock()
			return ExecutionGrantState{}, ErrAuthority
		}
		state, err := c.authorityStateLocked()
		c.mu.Unlock()
		return state, err
	}
	if c.policy.Version != grant.PolicyVersion || c.grant != nil && (!c.grant.Sealed || grant.Sequence <= c.grant.Grant.Sequence) || !time.Now().Before(deadline) {
		c.mu.Unlock()
		return ExecutionGrantState{}, ErrAuthority
	}
	for i, share := range []AuthorityShare{grant.Upload, grant.Download} {
		limit := c.policy.UploadRate
		if i == 1 {
			limit = c.policy.DownloadRate
		}
		if share.Unlimited && limit != 0 || !share.Unlimited && limit != 0 && share.Rate > limit || share.Burst > c.policy.BurstBytes {
			c.mu.Unlock()
			return ExecutionGrantState{}, ErrAuthority
		}
	}
	previous := c.grant
	c.grant = &runtimeAuthorityGrant{storedAuthorityGrant: storedAuthorityGrant{Grant: grant, StartUsage: c.usage}, deadline: deadline}
	if err := c.persistLocked(c.policy, c.revoked, 0); err != nil {
		c.grant = previous
		c.mu.Unlock()
		return ExecutionGrantState{}, e.storageFailed(err)
	}
	c.previousGrant = nil
	now := time.Now()
	c.buckets[Upload].update(grant.Upload.Rate, grant.Upload.Burst, now)
	c.buckets[Download].update(grant.Download.Rate, grant.Download.Burst, now)
	if c.grantExpiry != nil {
		c.grantExpiry.Stop()
	}
	c.grantExpiry = time.AfterFunc(time.Until(deadline), c.expire)
	c.notifyLocked()
	state, err := c.authorityStateLocked()
	c.mu.Unlock()
	return state, err
}

func (e *Engine) SealAuthorityGrant(clientID, grantID string) (ExecutionGrantState, error) {
	c, err := e.state(clientID)
	if err != nil {
		return ExecutionGrantState{}, err
	}
	c.mu.Lock()
	if e.failed.Load() || c.closed {
		c.mu.Unlock()
		return ExecutionGrantState{}, ErrStorage
	}
	if c.grant == nil || c.grant.Grant.BootID != e.bootID || c.grant.Grant.GrantID != grantID {
		c.mu.Unlock()
		return ExecutionGrantState{}, ErrAuthority
	}
	if !c.grant.Sealed {
		c.grant.Sealed = true
		if err := c.persistLocked(c.policy, c.revoked, 0); err != nil {
			c.mu.Unlock()
			return ExecutionGrantState{}, e.storageFailed(err)
		}
		c.notifyLocked()
		if c.grantExpiry != nil {
			c.grantExpiry.Stop()
		}
	}
	state, err := c.authorityStateLocked()
	sessions := c.sessionsLocked()
	c.mu.Unlock()
	closeSessions(sessions)
	return state, err
}

func (e *Engine) GetAuthorityGrant(clientID string) (ExecutionGrantState, error) {
	c, err := e.state(clientID)
	if err != nil {
		return ExecutionGrantState{}, err
	}
	c.mu.Lock()
	if e.failed.Load() {
		c.mu.Unlock()
		return ExecutionGrantState{}, ErrStorage
	}
	if c.grant == nil {
		c.mu.Unlock()
		return ExecutionGrantState{}, ErrAuthority
	}
	if c.checkpointDirty {
		if err := c.persistLocked(c.policy, c.revoked, 0); err != nil {
			c.mu.Unlock()
			return ExecutionGrantState{}, e.storageFailed(err)
		}
	}
	state, err := c.authorityStateLocked()
	c.mu.Unlock()
	return state, err
}

func validateStoredAuthorityGrant(record storedClient, instanceID string) error {
	if record.AuthorityGrant == nil {
		return nil
	}
	stored := record.AuthorityGrant
	spent, err := grantUsage(record.Usage, stored.StartUsage)
	reserved := Usage{BilledBytes: stored.ReservedBilledBytes, Remainder: stored.ReservedRemainder}
	if !validExecutionGrant(stored.Grant) || stored.Grant.InstanceID != instanceID || stored.Grant.ClientID != record.Policy.ClientID || stored.Grant.PolicyVersion > record.Policy.Version || err != nil || reserved.Remainder >= MultiplierScale || billedLess(reserved, spent) || billedLess(Usage{BilledBytes: stored.Grant.Capacity}, reserved) || stored.Sealed && (reserved.BilledBytes != spent.BilledBytes || reserved.Remainder != spent.Remainder) {
		return fmt.Errorf("%w: contradictory stored grant", ErrStorage)
	}
	return nil
}
