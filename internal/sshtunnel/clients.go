package sshtunnel

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type authenticatedSession struct {
	client Client
	key    string
	cancel context.CancelFunc
}

func validateClients(config []Client) (map[string]Client, error) {
	clients := make(map[string]Client)
	for _, client := range config {
		if _, err := uuid.Parse(client.PolicyID); err != nil || client.Username == "" || len(client.Username) > 64 || strings.ContainsAny(client.Username, " \t\r\n\x00") || len(client.PublicKeys) == 0 {
			return nil, ErrConfig
		}
		if _, exists := clients[client.Username]; exists {
			return nil, ErrConfig
		}
		keys := make([]ssh.PublicKey, 0, len(client.PublicKeys))
		for _, key := range client.PublicKeys {
			if key == nil {
				return nil, ErrConfig
			}
			if _, certificate := key.(*ssh.Certificate); certificate {
				return nil, ErrConfig
			}
			frozen, err := ssh.ParsePublicKey(key.Marshal())
			if err != nil {
				return nil, ErrConfig
			}
			keys = append(keys, frozen)
		}
		client.PublicKeys = keys
		client.Targets = append([]TargetRule(nil), client.Targets...)
		client.Reverse = append([]ReverseRule(nil), client.Reverse...)
		for n, rule := range client.Targets {
			if rule.Host == "*" {
				continue
			}
			host, err := canonicalHost(rule.Host)
			if err != nil {
				return nil, err
			}
			client.Targets[n].Host = host
		}
		for n, rule := range client.Reverse {
			address, err := netip.ParseAddr(rule.Address)
			if err != nil || address.Zone() != "" {
				return nil, ErrConfig
			}
			client.Reverse[n].Address = address.Unmap().String()
		}
		clients[client.Username] = client
	}
	return clients, nil
}

// Credential addition preserves sessions; revoked keys and changed access rules retire affected sessions.
func (s *Server) UpdateClients(config []Client) error {
	clients, err := validateClients(config)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return ErrClosed
	}
	s.clients = clients
	var revoked []net.Conn
	for conn, session := range s.sessions {
		next, exists := clients[session.client.Username]
		if !exists || next.PolicyID != session.client.PolicyID || !allowsFingerprint(next, session.key) || !slices.Equal(next.Targets, session.client.Targets) || !slices.Equal(next.Reverse, session.client.Reverse) {
			session.cancel()
			revoked = append(revoked, conn)
		}
	}
	s.mu.Unlock()
	for _, conn := range revoked {
		_ = conn.Close()
	}
	return nil
}

func allowsFingerprint(client Client, fingerprint string) bool {
	for _, key := range client.PublicKeys {
		if ssh.FingerprintSHA256(key) == fingerprint {
			return true
		}
	}
	return false
}

// Revalidate after the signed handshake so an in-flight authentication cannot revive a removed key.
func (s *Server) registerSession(raw net.Conn, conn *ssh.ServerConn) (Client, context.Context, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, exists := s.clients[conn.User()]
	if !exists || s.ctx.Err() != nil || conn.Permissions == nil || conn.Permissions.Extensions["policy-id"] != client.PolicyID || !allowsFingerprint(client, conn.Permissions.Extensions["auth-key"]) {
		return Client{}, nil, nil, false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.sessions[raw] = authenticatedSession{client: client, key: conn.Permissions.Extensions["auth-key"], cancel: cancel}
	return client, ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.sessions, raw)
		s.mu.Unlock()
	}, true
}
