package clientpolicy

import (
	"math"
	"time"
)

type AuthorityGrantRenewal struct {
	BootID        string
	ClientID      string
	GrantID       string
	ChallengeID   string
	Sequence      uint64
	LeaseDuration time.Duration
}

// Renewal extends enforcement while the authority retains the existing grant's
// entire remaining capacity and rate shares. It issues no allowance or receipt.
// Activation deadlines and renewal retries exist only in this boot's memory.
func (e *Engine) RenewAuthorityGrant(request AuthorityGrantRenewal) (time.Time, error) {
	if request.BootID != e.bootID || request.BootID == "" || !validInstanceID(request.ClientID) || !validInstanceID(request.GrantID) || request.Sequence == 0 || request.Sequence > math.MaxInt64 {
		return time.Time{}, ErrAuthority
	}
	deadline, err := e.authorityDeadline(request.ChallengeID, request.LeaseDuration)
	if err != nil {
		return time.Time{}, err
	}
	c, err := e.state(request.ClientID)
	if err != nil {
		return time.Time{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.failed.Load() || c.closed {
		return time.Time{}, ErrStorage
	}
	if c.grant == nil || c.grant.Sealed || c.grant.Grant.GrantID != request.GrantID || c.grant.Grant.BootID != request.BootID || c.grant.Grant.PolicyVersion != c.policy.Version || !time.Now().Before(c.grant.deadline) {
		return time.Time{}, ErrAuthority
	}
	if request.Sequence == c.grant.renewal.Sequence {
		if request != c.grant.renewal {
			return time.Time{}, ErrAuthority
		}
		return c.grant.deadline, nil
	}
	if request.Sequence < c.grant.renewal.Sequence || !deadline.After(c.grant.deadline) {
		return time.Time{}, ErrAuthority
	}
	c.grant.renewal, c.grant.deadline = request, deadline
	if c.grantExpiry != nil {
		c.grantExpiry.Stop()
	}
	c.grantExpiry = time.AfterFunc(time.Until(deadline), c.expire)
	c.notifyLocked()
	return deadline, nil
}
