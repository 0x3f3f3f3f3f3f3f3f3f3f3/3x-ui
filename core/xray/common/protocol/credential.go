package protocol

import (
	"errors"
	"sync"
)

var ErrCredentialRevoked = errors.New("credential revoked")

type credentialSession struct {
	close func()
}

type credentialState struct {
	mu       sync.Mutex
	revoked  bool
	sessions map[*credentialSession]struct{}
}

// TrackSession fences admission against removal of this authenticated credential.
func (u *MemoryUser) TrackSession(closeFn func()) (func(), error) {
	c := &u.credential
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revoked {
		return nil, ErrCredentialRevoked
	}
	if c.sessions == nil {
		c.sessions = make(map[*credentialSession]struct{})
	}
	s := &credentialSession{close: closeFn}
	c.sessions[s] = struct{}{}
	return func() {
		c.mu.Lock()
		delete(c.sessions, s)
		c.mu.Unlock()
	}, nil
}

func (u *MemoryUser) RevokeCredential() {
	c := &u.credential
	c.mu.Lock()
	c.revoked = true
	sessions := c.sessions
	c.sessions = nil
	c.mu.Unlock()
	for s := range sessions {
		s.close()
	}
}
