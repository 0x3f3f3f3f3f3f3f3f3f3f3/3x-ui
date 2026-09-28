package clientpolicy

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const maxClientSessions = 4096

type Engine struct {
	mu      sync.Mutex
	clients map[string]*clientState
	closed  bool
	nextID  atomic.Uint64
}

type clientState struct {
	mu       sync.Mutex
	policy   Policy
	usage    Usage
	revoked  bool
	closed   bool
	changed  chan struct{}
	buckets  [2]bucket
	sessions map[uint64]*Session
	expiry   *time.Timer
}

func NewEngine() *Engine { return &Engine{clients: make(map[string]*clientState)} }

func (e *Engine) state(id string) (*clientState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrEngineClosed
	}
	c := e.clients[id]
	if c == nil {
		return nil, ErrUnknownClient
	}
	return c, nil
}

func (e *Engine) Apply(p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrEngineClosed
	}
	c := e.clients[p.ClientID]
	if c == nil {
		c = &clientState{changed: make(chan struct{}), sessions: make(map[uint64]*Session)}
		e.clients[p.ClientID] = c
	}
	e.mu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrEngineClosed
	}
	if c.revoked {
		c.mu.Unlock()
		return ErrRevoked
	}
	if p.Version < c.policy.Version || p.Version == c.policy.Version && p != c.policy {
		c.mu.Unlock()
		return ErrPolicyVersion
	}
	if p == c.policy {
		c.mu.Unlock()
		return nil
	}
	now := time.Now()
	c.buckets[Upload].update(p.UploadRate, p.BurstBytes, now)
	c.buckets[Download].update(p.DownloadRate, p.BurstBytes, now)
	c.policy = p
	c.notifyLocked()
	if c.expiry != nil {
		c.expiry.Stop()
	}
	if p.ExpiresAt != 0 {
		c.expiry = time.AfterFunc(time.Until(time.UnixMilli(p.ExpiresAt)), c.expire)
	}
	var closeList []*Session
	if p.reasons(c.usage, false, now) != 0 {
		closeList = c.sessionsLocked()
	}
	c.mu.Unlock()
	closeSessions(closeList)
	return nil
}

func (c *clientState) notifyLocked() { close(c.changed); c.changed = make(chan struct{}) }

func (c *clientState) sessionsLocked() []*Session {
	out := make([]*Session, 0, len(c.sessions))
	for _, s := range c.sessions {
		out = append(out, s)
	}
	return out
}

func closeSessions(sessions []*Session) {
	for _, s := range sessions {
		s.Close()
	}
}

func (c *clientState) expire() {
	c.mu.Lock()
	var sessions []*Session
	if c.policy.reasons(c.usage, c.revoked, time.Now()) != 0 {
		sessions = c.sessionsLocked()
		c.notifyLocked()
	}
	c.mu.Unlock()
	closeSessions(sessions)
}

func (e *Engine) Snapshot(id string) (Snapshot, error) {
	c, err := e.state(id)
	if err != nil {
		return Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{Usage: c.usage, PolicyVersion: c.policy.Version, Reasons: c.policy.reasons(c.usage, c.revoked, time.Now()), ActiveSessions: len(c.sessions)}, nil
}

func (e *Engine) Remove(id string) error {
	c, err := e.state(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.revoked = true
	c.notifyLocked()
	if c.expiry != nil {
		c.expiry.Stop()
	}
	sessions := c.sessionsLocked()
	c.mu.Unlock()
	closeSessions(sessions)
	return nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	clients := make([]*clientState, 0, len(e.clients))
	for _, c := range e.clients {
		clients = append(clients, c)
	}
	e.mu.Unlock()
	for _, c := range clients {
		c.mu.Lock()
		c.closed = true
		c.notifyLocked()
		if c.expiry != nil {
			c.expiry.Stop()
		}
		sessions := c.sessionsLocked()
		c.mu.Unlock()
		closeSessions(sessions)
	}
	return nil
}

type Session struct {
	client      *clientState
	metadata    Metadata
	ctx         context.Context
	cancel      context.CancelFunc
	closeFn     func()
	closed      atomic.Bool
	cleanupMu   sync.Mutex
	stopContext func() bool
}

func (e *Engine) Open(ctx context.Context, metadata Metadata, closeFn func()) (*Session, error) {
	c, err := e.state(metadata.ClientID)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrEngineClosed
	}
	if c.policy.reasons(c.usage, c.revoked, time.Now()) != 0 {
		c.mu.Unlock()
		return nil, ErrRestricted
	}
	if len(c.sessions) >= maxClientSessions {
		c.mu.Unlock()
		return nil, ErrQueueFull
	}
	metadata.SessionID = e.nextID.Add(1)
	child, cancel := context.WithCancel(ctx)
	s := &Session{client: c, metadata: metadata, ctx: child, cancel: cancel, closeFn: closeFn}
	c.sessions[metadata.SessionID] = s
	c.mu.Unlock()
	stop := context.AfterFunc(ctx, s.Close)
	s.cleanupMu.Lock()
	if s.closed.Load() {
		stop()
	} else {
		s.stopContext = stop
	}
	s.cleanupMu.Unlock()
	return s, nil
}

func (s *Session) Close() {
	s.finish(true)
}

func (s *Session) Release() {
	s.finish(false)
}

func (s *Session) finish(terminate bool) {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.cancel()
	s.cleanupMu.Lock()
	stop := s.stopContext
	s.stopContext = nil
	s.cleanupMu.Unlock()
	if stop != nil {
		stop()
	}
	s.client.mu.Lock()
	delete(s.client.sessions, s.metadata.SessionID)
	s.client.mu.Unlock()
	if terminate && s.closeFn != nil {
		s.closeFn()
	}
}

func (s *Session) Admit(direction Direction, n uint64) error {
	if direction != Upload && direction != Download {
		return ErrInvalidDirection
	}
	if s.closed.Load() || s.ctx.Err() != nil {
		return ErrSessionClosed
	}
	c := s.client
	b := &c.buckets[direction]
	if b.pending.Add(1) > maxPendingAdmissions {
		b.pending.Add(-1)
		return ErrQueueFull
	}
	defer b.pending.Add(-1)
	select {
	case b.gate <- struct{}{}:
	case <-s.ctx.Done():
		return ErrSessionClosed
	}
	defer func() { <-b.gate }()
	for {
		c.mu.Lock()
		if s.closed.Load() || s.ctx.Err() != nil {
			c.mu.Unlock()
			return ErrSessionClosed
		}
		now := time.Now()
		if c.policy.reasons(c.usage, c.revoked, now) != 0 {
			sessions := c.sessionsLocked()
			c.mu.Unlock()
			closeSessions(sessions)
			return ErrRestricted
		}
		next, err := charge(c.usage, direction, n, c.policy.Multiplier)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		if q := c.policy.QuotaBytes; q != 0 && (next.BilledBytes > q || next.BilledBytes == q && next.Remainder != 0) {
			c.mu.Unlock()
			return ErrRestricted
		}
		delay, err := b.delay(n, now)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		if delay == 0 {
			if b.rate != 0 {
				b.tokens -= float64(n)
			}
			c.usage = next
			var sessions []*Session
			if c.policy.reasons(c.usage, c.revoked, now) != 0 {
				sessions = c.sessionsLocked()
				c.notifyLocked()
			}
			c.mu.Unlock()
			closeSessions(sessions)
			return nil
		}
		changed := c.changed
		c.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-changed:
		case <-s.ctx.Done():
			timer.Stop()
			return ErrSessionClosed
		}
		timer.Stop()
	}
}
