package mieru

import "net/netip"

type OnlineSession struct {
	PolicyID string
	Username string
	SourceIP netip.Addr
}

type ServerStatus struct {
	Listening             bool
	AuthenticatedSessions int
}

func (s *Server) OnlineSessions() []OnlineSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil
	}
	var sessions []OnlineSession
	for conn := range s.sessions {
		if !conn.admitted || conn.closing.Load() {
			continue
		}
		client := s.clients[conn.UserName()]
		if client == nil || client.ctx.Err() != nil {
			continue
		}
		source, err := netip.ParseAddrPort(conn.RemoteAddr().String())
		if err != nil || source.Addr().IsUnspecified() {
			continue
		}
		sessions = append(sessions, OnlineSession{PolicyID: client.PolicyID, Username: client.Username, SourceIP: source.Addr().Unmap()})
	}
	return sessions
}

func (s *Server) Status() ServerStatus {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	return ServerStatus{Listening: s.started && s.ctx.Err() == nil, AuthenticatedSessions: len(s.OnlineSessions())}
}

func (s *Server) admitSession(conn *managedSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	client := s.clients[conn.UserName()]
	if client == nil || client.ctx.Err() != nil || s.ctx.Err() != nil || conn.closing.Load() {
		return false
	}
	conn.admitted = true
	return true
}
