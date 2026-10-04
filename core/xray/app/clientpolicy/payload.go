package clientpolicy

import (
	"sync"
	"time"
)

// AdmitPayload retains the final already charged payload until its forwarder
// finishes delivery. Quota denies all further admission immediately; explicit
// disable, expiry, retirement and shutdown still close the session normally.
func (s *Session) AdmitPayload(direction Direction, n uint64) (func(), error) {
	c := s.client
	c.mu.Lock()
	if s.payloadPending >= maxClientSessions {
		c.mu.Unlock()
		return nil, ErrQueueFull
	}
	s.payloadPending++
	c.mu.Unlock()
	finish := sync.OnceFunc(func() {
		c.mu.Lock()
		s.payloadPending--
		now := time.Now()
		var closed []*Session
		if c.reasonsLocked(now) != 0 && !c.canRefillAuthorityLocked(now) && !c.transitionPendingLocked(now) {
			closed = c.restrictionSessionsLocked(now)
		}
		c.mu.Unlock()
		closeSessions(closed)
	})
	if err := s.Admit(direction, n); err != nil {
		finish()
		return nil, err
	}
	return finish, nil
}

// The caller holds the client lock. Only quota exhaustion can wait for an
// admitted payload; another restriction always terminates it immediately.
func (c *clientState) restrictionSessionsLocked(now time.Time) []*Session {
	sessions := c.sessionsLocked()
	reasons := c.reasonsLocked(now)
	if reasons&ReasonQuota == 0 || reasons & ^(ReasonQuota|ReasonAuthority) != 0 {
		return sessions
	}
	kept := sessions[:0]
	for _, session := range sessions {
		if session.payloadPending == 0 {
			kept = append(kept, session)
		}
	}
	return kept
}
