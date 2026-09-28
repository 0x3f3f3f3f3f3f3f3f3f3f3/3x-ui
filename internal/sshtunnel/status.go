package sshtunnel

type Status struct {
	Listening                bool
	AuthenticatedConnections int
}

func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return Status{}
	}
	return Status{Listening: s.serving, AuthenticatedConnections: len(s.sessions)}
}
