package clientpolicy

import (
	"math"
	"time"
)

func (p Policy) effectiveExpiry(firstUsedAt int64) (int64, error) {
	if firstUsedAt < 0 || p.ExpiresAt == math.MinInt64 {
		return 0, ErrInvalidPolicy
	}
	if p.ExpiresAt >= 0 {
		return p.ExpiresAt, nil
	}
	if firstUsedAt == 0 {
		return 0, nil
	}
	if firstUsedAt > math.MaxInt64+p.ExpiresAt {
		return 0, ErrOverflow
	}
	return firstUsedAt - p.ExpiresAt, nil
}

func (c *clientState) firstUseForPolicyLocked(p Policy) int64 {
	if p.ExpiresAt < 0 && p.ExpiresAt != c.policy.ExpiresAt {
		return 0
	}
	return c.firstUsedAt
}

func (c *clientState) armExpiryLocked() {
	if c.expiry != nil {
		c.expiry.Stop()
		c.expiry = nil
	}
	at, err := c.policy.effectiveExpiry(c.firstUsedAt)
	if err != nil {
		at = 1
	}
	if at > 0 && !c.revoked && !c.closed {
		c.expiry = time.AfterFunc(time.Until(time.UnixMilli(at)), c.expire)
	}
}
