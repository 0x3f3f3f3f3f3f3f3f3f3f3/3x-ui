package mieru

import (
	"net"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/protocol"
)

type underlayConnection struct {
	net.Conn
	server   *Server
	mu       sync.Mutex
	user     *protocol.MemoryUser
	deadline time.Time
	closed   bool
	timer    *time.Timer
	bindings map[string]*protocol.MemoryUser
}

func (c *underlayConnection) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.user == nil && (deadline.IsZero() || deadline.After(c.deadline)) {
		deadline = c.deadline
	}
	return c.Conn.SetReadDeadline(deadline)
}

func (c *underlayConnection) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
	c.server.physicalMu.Lock()
	if c.server.physical[c.RemoteAddr().String()] == c {
		delete(c.server.physical, c.RemoteAddr().String())
	}
	c.server.physicalMu.Unlock()
	return c.Conn.Close()
}

type underlayListener struct {
	net.Listener
	server *Server
}

func (l *underlayListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		limit := int(l.server.config.MaxConnections)
		if limit == 0 {
			limit = 1024
		}
		timeout := l.server.config.HandshakeTimeoutSeconds
		if timeout == 0 {
			timeout = 10
		}
		l.server.authMu.Lock()
		l.server.physicalMu.Lock()
		if len(l.server.physical) >= limit {
			l.server.physicalMu.Unlock()
			l.server.authMu.Unlock()
			_ = conn.Close()
			continue
		}
		owned := &underlayConnection{Conn: conn, server: l.server, deadline: time.Now().Add(time.Duration(timeout) * time.Second), bindings: make(map[string]*protocol.MemoryUser)}
		for _, user := range l.server.users.GetUsers() {
			owned.bindings[user.Account.(*Account).Username] = user
		}
		owned.timer = time.AfterFunc(time.Duration(timeout)*time.Second, func() { _ = owned.Close() })
		l.server.physical[conn.RemoteAddr().String()] = owned
		l.server.physicalMu.Unlock()
		l.server.authMu.Unlock()
		_ = owned.SetReadDeadline(owned.deadline)
		return owned, nil
	}
}

func (s *Server) bindUnderlay(conn net.Conn, user *protocol.MemoryUser) bool {
	s.physicalMu.Lock()
	raw := s.physical[conn.RemoteAddr().String()]
	if raw == nil {
		s.physicalMu.Unlock()
		return false
	}
	raw.mu.Lock()
	defer raw.mu.Unlock()
	defer s.physicalMu.Unlock()
	if raw.closed || raw.user != nil && raw.user != user || raw.user == nil && raw.bindings[user.Account.(*Account).Username] != user {
		return false
	}
	raw.user = user
	if raw.timer != nil {
		raw.timer.Stop()
	}
	raw.bindings = nil
	return raw.Conn.SetReadDeadline(time.Time{}) == nil
}

// An unbound underlay may contain a pending old authentication and must be
// discarded during credential changes. Already bound sibling users survive.
func (s *Server) closeUnderlays(email string) {
	s.physicalMu.Lock()
	var closing []*underlayConnection
	for _, conn := range s.physical {
		conn.mu.Lock()
		close := conn.user == nil || email != "" && conn.user.Email == email
		conn.mu.Unlock()
		if close {
			closing = append(closing, conn)
		}
	}
	s.physicalMu.Unlock()
	for _, conn := range closing {
		_ = conn.Close()
	}
}
