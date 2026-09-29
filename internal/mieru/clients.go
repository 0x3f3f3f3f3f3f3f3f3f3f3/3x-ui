package mieru

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/enfein/mieru/v3/pkg/cipher"
	"github.com/google/uuid"
)

type clientGeneration struct {
	Client
	identity string
	user     *appctlpb.User
	ctx      context.Context
	cancel   context.CancelFunc
}

func validateClients(config []Client) (map[string]Client, error) {
	clients := make(map[string]Client, len(config))
	for _, client := range config {
		user := &appctlpb.User{Name: &client.Username, Password: &client.Password}
		if client.PolicyID == "" || strings.TrimSpace(client.Username) != client.Username || appctlcommon.ValidateServerConfigSingleUser(user) != nil {
			return nil, ErrConfig
		}
		if _, exists := clients[client.Username]; exists {
			return nil, ErrConfig
		}
		clients[client.Username] = client
	}
	return clients, nil
}

func (s *Server) replaceClients(config map[string]Client) []*managedSession {
	previous := make(map[string]*clientGeneration, len(s.clients))
	for _, client := range s.clients {
		previous[client.Username] = client
	}
	clients := make(map[string]*clientGeneration, len(config))
	users := make(map[string]*appctlpb.User, len(config))
	for username, client := range config {
		generation := previous[username]
		if generation == nil || generation.Client != client {
			identity := uuid.NewString()
			// The native cipher keeps this identity; hashing retains the external client's username salt.
			hashed := hex.EncodeToString(cipher.HashPassword([]byte(client.Password), []byte(username)))
			ctx, cancel := context.WithCancel(s.ctx)
			generation = &clientGeneration{Client: client, identity: identity, user: &appctlpb.User{Name: &identity, HashedPassword: &hashed}, ctx: ctx, cancel: cancel}
		}
		clients[generation.identity], users[generation.identity] = generation, generation.user
	}
	if s.mux != nil {
		s.mux.SetServerUsers(users)
	}
	for identity, client := range s.clients {
		if clients[identity] == nil {
			client.cancel()
		}
	}
	s.clients, s.users = clients, users
	var revoked []*managedSession
	for conn := range s.sessions {
		if identity := conn.UserName(); identity != "" && clients[identity] == nil {
			revoked = append(revoked, conn)
		}
	}
	return revoked
}

// UpdateClients atomically replaces credentials and retires changed authentication generations.
func (s *Server) UpdateClients(config []Client) error {
	clients, err := validateClients(config)
	if err != nil {
		return err
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.ctx.Err() != nil {
		return ErrClosed
	}
	s.mu.Lock()
	revoked := s.replaceClients(clients)
	s.mu.Unlock()
	for _, conn := range revoked {
		_ = conn.Close()
	}
	s.retireTransports(revoked)
	return nil
}

func (s *Server) retireTransports(sessions []*managedSession) {
	retired := s.listeners.retired(s.clients)
	for _, conn := range sessions {
		if conn.transport != nil {
			retired[conn.transport] = struct{}{}
		}
	}
	if len(retired) == 0 {
		return
	}
	s.workers.Go(func() {
		defer func() {
			for conn := range retired {
				_ = conn.Close()
			}
		}()
		// Let logical close messages reach the client before aborting its cached TCP cipher.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		for _, conn := range sessions {
			select {
			case <-conn.done:
			case <-timer.C:
				return
			}
		}
	})
}

func (s *Server) authenticatedClient(conn *managedSession) *clientGeneration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil
	}
	client := s.clients[conn.UserName()]
	if client != nil {
		s.listeners.identify(conn.transport, client.identity)
	}
	return client
}
