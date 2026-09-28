package sshtunnel

import (
	"net"
	"net/netip"
)

type OnlineSession struct {
	PolicyID string
	Username string
	SourceIP netip.Addr
}

func (s *Server) OnlineSessions() []OnlineSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil
	}
	var online []OnlineSession
	for conn, session := range s.sessions {
		if !session.admitted || session.ctx.Err() != nil {
			continue
		}
		peer, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok {
			continue
		}
		ip := peer.AddrPort().Addr().Unmap()
		if !ip.IsValid() || ip.IsUnspecified() {
			continue
		}
		online = append(online, OnlineSession{PolicyID: session.client.PolicyID, Username: session.client.Username, SourceIP: ip})
	}
	return online
}

func (s *Server) admitSession(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[conn]; ok && session.ctx.Err() == nil {
		session.admitted = true
		s.sessions[conn] = session
	}
}
