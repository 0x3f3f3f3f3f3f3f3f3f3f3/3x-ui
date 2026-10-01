package ssh

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"strings"
	"sync"

	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy"
	gossh "golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/proto"
)

type userEntry struct {
	user        *protocol.MemoryUser
	keys        map[string]struct{}
	password    [32]byte
	hasPassword bool
}

type userStore struct {
	mu         sync.RWMutex
	closed     bool
	byUsername map[string]*userEntry
	byEmail    map[string]*userEntry
}

var _ proxy.UserManager = (*Server)(nil)

func (s *Server) AddUser(_ context.Context, user *protocol.MemoryUser) error {
	if user == nil || user.ClientID == "" || user.Email == "" {
		return ErrConfiguration
	}
	a, ok := user.Account.(*Account)
	if !ok || a == nil || a.Username == "" || len(a.Username) > 256 || len(a.Password) > 1024 || len(a.PublicKeys) > 16 {
		return ErrConfiguration
	}
	copyAccount := proto.Clone(a).(*Account)
	entry := &userEntry{user: &protocol.MemoryUser{Account: copyAccount, ClientID: user.ClientID, Email: user.Email, Level: user.Level}, keys: make(map[string]struct{}), hasPassword: a.Password != "", password: sha256.Sum256([]byte(a.Password))}
	for _, line := range a.PublicKeys {
		if len(line) > 16384 {
			return ErrConfiguration
		}
		key, _, options, rest, err := gossh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(options) != 0 || strings.TrimSpace(string(rest)) != "" {
			return ErrConfiguration
		}
		if _, cert := key.(*gossh.Certificate); cert {
			return ErrConfiguration
		}
		entry.keys[string(key.Marshal())] = struct{}{}
	}
	if len(entry.keys) == 0 && (!s.allowPassword || !entry.hasPassword) {
		return ErrConfiguration
	}
	s.users.mu.Lock()
	defer s.users.mu.Unlock()
	if s.users.closed || len(s.users.byUsername) >= 4096 {
		return ErrResourceLimit
	}
	if s.users.byUsername[a.Username] != nil || s.users.byEmail[user.Email] != nil {
		return ErrConfiguration
	}
	s.users.byUsername[a.Username] = entry
	s.users.byEmail[user.Email] = entry
	return nil
}

func (s *Server) keyUser(username string, key gossh.PublicKey) *protocol.MemoryUser {
	s.users.mu.RLock()
	defer s.users.mu.RUnlock()
	entry := s.users.byUsername[username]
	if entry != nil {
		if _, ok := entry.keys[string(key.Marshal())]; ok {
			return entry.user
		}
	}
	return nil
}

func (s *Server) passwordUser(username string, password []byte) *protocol.MemoryUser {
	digest := sha256.Sum256(password)
	s.users.mu.RLock()
	defer s.users.mu.RUnlock()
	entry := s.users.byUsername[username]
	want := sha256.Sum256(nil)
	if entry != nil {
		want = entry.password
	}
	match := subtle.ConstantTimeCompare(digest[:], want[:]) == 1
	if match && entry != nil && entry.hasPassword && len(password) <= 1024 {
		return entry.user
	}
	return nil
}

func (s *Server) RemoveUser(_ context.Context, email string) error {
	s.users.mu.Lock()
	entry := s.users.byEmail[email]
	if entry != nil {
		delete(s.users.byEmail, email)
		delete(s.users.byUsername, entry.user.Account.(*Account).Username)
	}
	s.users.mu.Unlock()
	if entry == nil {
		return ErrAuthentication
	}
	entry.user.RevokeCredential()
	return nil
}

func (s *Server) GetUser(_ context.Context, email string) *protocol.MemoryUser {
	s.users.mu.RLock()
	defer s.users.mu.RUnlock()
	if e := s.users.byEmail[email]; e != nil {
		return e.user
	}
	return nil
}

func (s *Server) GetUsers(_ context.Context) []*protocol.MemoryUser {
	s.users.mu.RLock()
	defer s.users.mu.RUnlock()
	out := make([]*protocol.MemoryUser, 0, len(s.users.byEmail))
	for _, e := range s.users.byEmail {
		out = append(out, e.user)
	}
	return out
}

func (s *Server) GetUsersCount(_ context.Context) int64 {
	s.users.mu.RLock()
	defer s.users.mu.RUnlock()
	return int64(len(s.users.byEmail))
}
