package clientpolicy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const maxClientSessions = 4096

type Engine struct {
	bootID           string
	authorityMu      sync.Mutex
	challenges       map[string]time.Time
	authorityBinding AuthorityBinding
	ready            atomic.Bool
	initial          []Policy
	startOnce        sync.Once
	startErr         error
	store            stateStore
	instanceID       string
	epoch            uint64
	failed           atomic.Bool
	mu               sync.Mutex
	clients          map[string]*clientState
	closed           bool
	nextID           atomic.Uint64
}

type clientState struct {
	grant              *runtimeAuthorityGrant
	previousGrant      *storedAuthorityGrant
	grantExpiry        *time.Timer
	firstUsedAt        int64
	initializationHash string
	engine             *Engine
	uncertain          uint64
	sequence           uint64
	reservationLeft    uint64
	checkpointDirty    bool
	mu                 sync.Mutex
	policy             Policy
	usage              Usage
	revoked            bool
	closed             bool
	changed            chan struct{}
	buckets            [2]bucket
	sessions           map[uint64]*Session
	expiry             *time.Timer
}

func newClientState(e *Engine) *clientState {
	return &clientState{engine: e, changed: make(chan struct{}), sessions: make(map[uint64]*Session)}
}

func NewEngine() *Engine {
	e := &Engine{clients: make(map[string]*clientState)}
	e.ready.Store(true)
	return e
}

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

func (e *Engine) Apply(p Policy) error { return e.ApplyBatch([]Policy{p}) }

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
	if c.reasonsLocked(time.Now()) != 0 {
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
	return Snapshot{FirstUsedAt: c.firstUsedAt, InstanceID: e.instanceID, Epoch: e.epoch, Sequence: c.sequence, UncertainBytes: c.uncertain, Usage: c.usage, PolicyVersion: c.policy.Version, Reasons: c.reasonsLocked(time.Now()), ActiveSessions: len(c.sessions)}, nil
}

func (e *Engine) Remove(id string) error { return e.RemoveVersion(id, 0) }

func (e *Engine) RemoveVersion(id string, version uint64) error {
	c, err := e.state(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if e.failed.Load() {
		c.mu.Unlock()
		return ErrStorage
	}
	if c.closed {
		c.mu.Unlock()
		return ErrEngineClosed
	}
	if version != 0 && version != c.policy.Version {
		c.mu.Unlock()
		return ErrPolicyVersion
	}
	if err := c.persistLocked(c.policy, true, 0); err != nil {
		c.mu.Unlock()
		return e.storageFailed(err)
	}
	c.revoked = true
	c.notifyLocked()
	if c.expiry != nil {
		c.expiry.Stop()
	}
	if c.grantExpiry != nil {
		c.grantExpiry.Stop()
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
	e.ready.Store(false)
	clients := make([]*clientState, 0, len(e.clients))
	for _, c := range e.clients {
		clients = append(clients, c)
	}
	e.mu.Unlock()
	var result error
	for _, c := range clients {
		c.mu.Lock()
		c.closed = true
		if !e.failed.Load() {
			if err := c.persistLocked(c.policy, c.revoked, 0); err != nil {
				e.failed.Store(true)
				result = err
			}
		}
		c.notifyLocked()
		if c.expiry != nil {
			c.expiry.Stop()
		}
		if c.grantExpiry != nil {
			c.grantExpiry.Stop()
		}
		sessions := c.sessionsLocked()
		c.mu.Unlock()
		closeSessions(sessions)
	}
	if e.store != nil {
		result = errors.Join(result, e.store.close())
	}
	if e.failed.Load() {
		result = errors.Join(result, ErrStorage)
	}
	return result
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
	if !e.ready.Load() {
		return nil, ErrEngineNotStarted
	}
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
	if c.reasonsLocked(time.Now()) != 0 {
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
		if c.reasonsLocked(now) != 0 {
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
		if c.policy.exceedsQuota(next, c.uncertain) || !c.withinAuthorityGrantLocked(next) {
			c.mu.Unlock()
			return ErrRestricted
		}
		if c.engine.bootID != "" {
			share := c.grant.Grant.Upload
			if direction == Download {
				share = c.grant.Grant.Download
			}
			if !share.Unlimited && share.Rate == 0 {
				c.mu.Unlock()
				return ErrRestricted
			}
		}
		delay, err := b.delay(n, now)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		if delay == 0 {
			starting := n > 0 && c.policy.ExpiresAt < 0 && c.firstUsedAt == 0
			if starting {
				if _, err := c.policy.effectiveExpiry(now.UnixMilli()); err != nil {
					c.mu.Unlock()
					return err
				}
				c.firstUsedAt = now.UnixMilli()
			}
			if err := c.reserveLocked(n); err != nil {
				if starting {
					c.firstUsedAt = 0
				}
				c.mu.Unlock()
				if errors.Is(err, ErrStorage) {
					return c.engine.storageFailed(err)
				}
				return err
			}
			if starting {
				c.armExpiryLocked()
			}
			if c.engine.store != nil {
				c.reservationLeft -= n
				c.checkpointDirty = true
			}
			if b.rate != 0 {
				b.tokens -= float64(n)
			}
			c.usage = next
			var sessions []*Session
			if c.reasonsLocked(now) != 0 {
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
