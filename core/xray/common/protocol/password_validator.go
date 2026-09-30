package protocol

import (
	"crypto/subtle"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
)

type passwordCredential struct {
	password string
	user     *MemoryUser
}

// PasswordValidator resolves verified usernames to immutable server identities.
// Aliases with one email must have the same stable owner; removing that email
// revokes all its credentials, without holding the lookup lock during closure.
type PasswordValidator struct {
	mu     sync.RWMutex
	closed bool
	users  map[string]passwordCredential
	emails map[string]map[string]*MemoryUser
}

func NewPasswordValidator(accounts, clientIDs, emails map[string]string, level uint32, account func(string, string) Account) (*PasswordValidator, error) {
	for username := range clientIDs {
		if _, exists := accounts[username]; !exists {
			return nil, errors.New("password identity has no authenticated account")
		}
	}
	for username := range emails {
		if _, exists := accounts[username]; !exists || clientIDs[username] == "" {
			return nil, errors.New("password email requires an authenticated managed account")
		}
	}
	v := &PasswordValidator{users: make(map[string]passwordCredential), emails: make(map[string]map[string]*MemoryUser)}
	for username, password := range accounts {
		email := emails[username]
		if email == "" {
			email = username
		}
		user := &MemoryUser{Account: account(username, password), ClientID: clientIDs[username], Email: email, Level: level}
		if err := v.Add(username, password, user); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func (v *PasswordValidator) Add(username, password string, user *MemoryUser) error {
	if user == nil || user.Account == nil || user.ClientID != "" && user.Email == "" {
		return errors.New("password account requires an authenticated identity")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return net.ErrClosed
	}
	if _, exists := v.users[username]; exists {
		return errors.New("password username already exists")
	}
	group := v.emails[user.Email]
	for _, existing := range group {
		if existing.ClientID != user.ClientID {
			return errors.New("password email has a different stable owner")
		}
	}
	if group == nil {
		group = make(map[string]*MemoryUser)
		v.emails[user.Email] = group
	}
	v.users[username] = passwordCredential{password: password, user: user}
	group[username] = user
	return nil
}

func (v *PasswordValidator) Authenticate(username, password string) *MemoryUser {
	v.mu.RLock()
	defer v.mu.RUnlock()
	credential, exists := v.users[username]
	if !exists || subtle.ConstantTimeCompare([]byte(credential.password), []byte(password)) != 1 {
		return nil
	}
	return credential.user
}

// emailLocked preserves exact legacy labels; a folded lookup must be unique.
func (v *PasswordValidator) emailLocked(email string) (string, error) {
	if _, exists := v.emails[email]; exists {
		return email, nil
	}
	var match string
	found := false
	for existing := range v.emails {
		if strings.EqualFold(existing, email) {
			if found {
				return "", errors.New("password email lookup is ambiguous")
			}
			match, found = existing, true
		}
	}
	if !found {
		return "", errors.New("password account not found")
	}
	return match, nil
}

func (v *PasswordValidator) Remove(email string) error {
	if email == "" {
		return errors.New("password account email must not be empty")
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return net.ErrClosed
	}
	key, err := v.emailLocked(email)
	if err != nil {
		v.mu.Unlock()
		return err
	}
	group := v.emails[key]
	delete(v.emails, key)
	for username := range group {
		delete(v.users, username)
	}
	v.mu.Unlock()
	for _, user := range group {
		user.RevokeCredential()
	}
	return nil
}

func (v *PasswordValidator) GetUser(email string) *MemoryUser {
	v.mu.RLock()
	defer v.mu.RUnlock()
	key, err := v.emailLocked(email)
	if err != nil {
		return nil
	}
	var first string
	var user *MemoryUser
	for username, entry := range v.emails[key] {
		if user == nil || username < first {
			first, user = username, entry
		}
	}
	return user
}

func (v *PasswordValidator) GetUsers() []*MemoryUser {
	v.mu.RLock()
	defer v.mu.RUnlock()
	names := make([]string, 0, len(v.users))
	for username := range v.users {
		names = append(names, username)
	}
	slices.Sort(names)
	users := make([]*MemoryUser, 0, len(names))
	for _, username := range names {
		users = append(users, v.users[username].user)
	}
	return users
}

func (v *PasswordValidator) GetCount() int64 {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return int64(len(v.users))
}

func (v *PasswordValidator) Close() error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	users := v.users
	v.users, v.emails = nil, nil
	v.mu.Unlock()
	for _, credential := range users {
		credential.user.RevokeCredential()
	}
	return nil
}
