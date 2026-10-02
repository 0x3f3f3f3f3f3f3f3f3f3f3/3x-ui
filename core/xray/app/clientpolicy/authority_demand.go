package clientpolicy

import (
	"context"
	"sort"
	"time"
)

const maxAuthorityRequestWaiters = 128
const maxAuthorityRequestReaders = 8
const maxAuthorityRequestWait = 2 * time.Second

type AuthorityRequest struct {
	RequestID       string
	ClientID        string
	PolicyVersion   uint64
	PreviousGrantID string
}

type queuedAuthorityRequest struct {
	request AuthorityRequest
	waiters int
}

func (e *Engine) checkAuthorityRequestBinding(expectedBootID string, binding AuthorityBinding) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.checkAuthorityRequestBindingLocked(expectedBootID, binding)
}

func (e *Engine) checkAuthorityRequestBindingLocked(expectedBootID string, binding AuthorityBinding) error {
	if e.closed {
		return ErrEngineClosed
	}
	if e.failed.Load() {
		return ErrStorage
	}
	if !e.ready.Load() {
		return ErrEngineNotStarted
	}
	if e.bootID == "" || e.bootID != expectedBootID || !validAuthorityBinding(binding) {
		return ErrAuthority
	}
	e.authorityMu.Lock()
	defer e.authorityMu.Unlock()
	if e.authorityBinding != binding {
		return ErrAuthority
	}
	return nil
}

func (e *Engine) notifyDemandLocked() {
	if e.demandChanged != nil {
		close(e.demandChanged)
	}
	e.demandChanged = make(chan struct{})
}

func (e *Engine) EnableAuthorityRequests(expectedBootID string, binding AuthorityBinding) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkAuthorityRequestBindingLocked(expectedBootID, binding); err != nil {
		return err
	}
	e.demandMu.Lock()
	defer e.demandMu.Unlock()
	if !e.demandEnabled {
		e.demandEnabled = true
		e.demandRequests = make(map[string]*queuedAuthorityRequest)
		e.notifyDemandLocked()
	}
	return nil
}

func (e *Engine) WaitAuthorityRequests(ctx context.Context, expectedBootID string, binding AuthorityBinding, limit int) ([]AuthorityRequest, error) {
	if ctx == nil || limit < 1 || limit > maxAuthorityRequestWaiters {
		return nil, ErrAuthority
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := e.checkAuthorityRequestBinding(expectedBootID, binding); err != nil {
			return nil, err
		}
		e.demandMu.Lock()
		// Recheck while registering the wait, so shutdown/failure cannot notify
		// an earlier channel and leave this reader waiting on a new one.
		if e.failed.Load() {
			e.demandMu.Unlock()
			return nil, ErrStorage
		}
		if !e.ready.Load() {
			e.demandMu.Unlock()
			return nil, ErrEngineClosed
		}
		if !e.demandEnabled {
			e.demandMu.Unlock()
			return nil, ErrAuthority
		}
		requests := make([]AuthorityRequest, 0, min(limit, len(e.demandRequests)))
		for _, pending := range e.demandRequests {
			requests = append(requests, pending.request)
		}
		if len(requests) > 0 {
			e.demandMu.Unlock()
			sort.Slice(requests, func(i, j int) bool { return requests[i].ClientID < requests[j].ClientID })
			return requests[:min(limit, len(requests))], nil
		}
		changed := e.demandChanged
		if e.demandReaders >= maxAuthorityRequestReaders {
			e.demandMu.Unlock()
			return nil, ErrQueueFull
		}
		e.demandReaders++
		e.demandMu.Unlock()
		var waitErr error
		select {
		case <-changed:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
		e.demandMu.Lock()
		e.demandReaders--
		e.demandMu.Unlock()
		if waitErr != nil {
			return nil, waitErr
		}
	}
}

// Entry and return both hold the client mutex. A demand is control traffic;
// only a durably installed, current grant can make the later Open succeed.
func (c *clientState) awaitAuthorityRequestLocked(ctx context.Context) error {
	e := c.engine
	e.demandMu.Lock()
	if !e.ready.Load() {
		e.demandMu.Unlock()
		return ErrEngineClosed
	}
	if !e.demandEnabled || c.reasonsLocked(time.Now()) != ReasonAuthority {
		e.demandMu.Unlock()
		return nil
	}
	if e.demandWaiters >= maxAuthorityRequestWaiters {
		e.demandMu.Unlock()
		return ErrQueueFull
	}
	previous := ""
	if c.grant != nil {
		previous = c.grant.Grant.GrantID
	}
	if c.demandRequest == nil || c.demandRequest.PolicyVersion != c.policy.Version || c.demandRequest.PreviousGrantID != previous {
		nonce, err := freshAuthorityNonce()
		if err != nil {
			e.demandMu.Unlock()
			return err
		}
		c.demandRequest = &AuthorityRequest{RequestID: nonce, ClientID: c.policy.ClientID, PolicyVersion: c.policy.Version, PreviousGrantID: previous}
	}
	request := *c.demandRequest
	pending := e.demandRequests[request.ClientID]
	if pending == nil {
		pending = &queuedAuthorityRequest{request: request}
		e.demandRequests[request.ClientID] = pending
		e.notifyDemandLocked()
	} else if pending.request != request {
		e.demandMu.Unlock()
		return ErrPolicyVersion
	}
	e.demandWaiters++
	pending.waiters++
	e.demandMu.Unlock()
	defer func() {
		e.demandMu.Lock()
		e.demandWaiters--
		pending.waiters--
		if pending.waiters == 0 && e.demandRequests[request.ClientID] == pending {
			delete(e.demandRequests, request.ClientID)
			e.notifyDemandLocked()
		}
		e.demandMu.Unlock()
	}()
	timer := time.NewTimer(maxAuthorityRequestWait)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.policy.Version != request.PolicyVersion {
			return ErrPolicyVersion
		}
		if c.closed {
			return ErrEngineClosed
		}
		if c.reasonsLocked(time.Now()) != ReasonAuthority {
			return nil
		}
		changed := c.changed
		c.mu.Unlock()
		var err error
		select {
		case <-changed:
		case <-ctx.Done():
			err = ctx.Err()
		case <-timer.C:
			err = ErrAuthority
		}
		c.mu.Lock()
		if err != nil {
			return err
		}
	}
}
