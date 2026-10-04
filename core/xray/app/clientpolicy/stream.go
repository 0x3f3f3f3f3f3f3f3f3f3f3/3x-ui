package clientpolicy

import "time"

// StreamChunkSize bounds one TCP admission by its current directional burst,
// affordable policy/grant allowance and the durable reservation quantum.
func (s *Session) StreamChunkSize(direction Direction) (uint64, error) {
	if direction != Upload && direction != Download {
		return 0, ErrInvalidDirection
	}
	if s.closed.Load() || s.ctx.Err() != nil {
		return 0, ErrSessionClosed
	}
	c := s.client
	c.mu.Lock()
	var closed []*Session
	defer func() { c.mu.Unlock(); closeSessions(closed) }()
	for {
		if s.closed.Load() || s.ctx.Err() != nil {
			return 0, ErrSessionClosed
		}
		waiting, err := s.awaitGrantTransitionLocked(time.Now())
		if err != nil {
			return 0, err
		}
		if !waiting {
			if c.reasonsLocked(time.Now()) == ReasonAuthority {
				refilled, err := c.refillAuthorityLocked(s.ctx)
				err = s.normalizeRefillErrorLocked(err)
				if err != nil {
					closed = c.failedRefillSessionsLocked()
					return 0, err
				}
				if refilled {
					continue
				}
			}
			break
		}
	}
	if c.reasonsLocked(time.Now()) != 0 {
		closed = c.restrictionSessionsLocked(time.Now())
		return 0, ErrRestricted
	}
	if c.engine.bootID != "" {
		share := c.grant.Grant.Upload
		if direction == Download {
			share = c.grant.Grant.Download
		}
		if !share.Unlimited && share.Rate == 0 {
			return 0, ErrRestricted
		}
	}
	limit := uint64(reservationRawBytes)
	b := &c.buckets[direction]
	if b.rate != 0 && b.burst < limit {
		limit = b.burst
	}
	lo, hi := uint64(0), limit
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		next, err := charge(c.usage, direction, mid, c.policy.Multiplier)
		if err != nil || c.policy.exceedsQuota(next, c.uncertain) || !c.withinAuthorityGrantLocked(next) {
			hi = mid - 1
		} else {
			lo = mid
		}
	}
	if lo == 0 {
		return 0, ErrRestricted
	}
	return lo, nil
}
