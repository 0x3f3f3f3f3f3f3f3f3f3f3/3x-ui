package clientpolicy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

var ErrAuthority = errors.New("missing, expired or mismatched client authority")

const MaxAuthorityLeaseDuration = 10 * time.Second
const maxAuthorityChallenges = 128

func freshAuthorityNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

type AuthorityChallenge struct {
	InstanceID  string
	BootID      string
	ChallengeID string
}

func (e *Engine) BeginAuthorityChallenge(expectedBootID string) (AuthorityChallenge, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return AuthorityChallenge{}, ErrEngineClosed
	}
	if !e.ready.Load() {
		return AuthorityChallenge{}, ErrEngineNotStarted
	}
	if e.failed.Load() {
		return AuthorityChallenge{}, ErrStorage
	}
	if e.bootID == "" || expectedBootID != e.bootID {
		return AuthorityChallenge{}, ErrAuthority
	}
	e.authorityMu.Lock()
	defer e.authorityMu.Unlock()
	now := time.Now()
	for id, born := range e.challenges {
		if !now.Before(born.Add(MaxAuthorityLeaseDuration)) {
			delete(e.challenges, id)
		}
	}
	if len(e.challenges) >= maxAuthorityChallenges {
		return AuthorityChallenge{}, ErrQueueFull
	}
	nonce, err := freshAuthorityNonce()
	if err != nil {
		return AuthorityChallenge{}, err
	}
	if e.challenges == nil {
		e.challenges = make(map[string]time.Time)
	}
	e.challenges[nonce] = now
	return AuthorityChallenge{InstanceID: e.instanceID, BootID: e.bootID, ChallengeID: nonce}, nil
}

func (e *Engine) authorityDeadline(challengeID string, duration time.Duration) (time.Time, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return time.Time{}, ErrEngineClosed
	}
	if duration <= 0 || duration > MaxAuthorityLeaseDuration {
		return time.Time{}, ErrAuthority
	}
	e.authorityMu.Lock()
	defer e.authorityMu.Unlock()
	born, exists := e.challenges[challengeID]
	if !exists {
		return time.Time{}, ErrAuthority
	}
	deadline := born.Add(duration)
	if !time.Now().Before(deadline) {
		return time.Time{}, ErrAuthority
	}
	return deadline, nil
}
