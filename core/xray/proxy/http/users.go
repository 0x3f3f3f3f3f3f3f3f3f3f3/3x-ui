package http

import (
	"context"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy"
)

var _ proxy.UserManager = (*Server)(nil)

func (s *Server) AddUser(_ context.Context, user *protocol.MemoryUser) error {
	if !s.authRequired {
		return errors.New("HTTP account creation requires password authentication mode")
	}
	if user == nil {
		return errors.New("HTTP account is required")
	}
	account, ok := user.Account.(*Account)
	if !ok || account == nil {
		return errors.New("HTTP account type is required")
	}
	if user.Email == "" {
		user = &protocol.MemoryUser{Account: account, Email: account.Username, Level: user.Level, ClientID: user.ClientID}
	}
	return s.users.Add(account.Username, account.Password, user)
}

func (s *Server) RemoveUser(_ context.Context, email string) error {
	return s.users.Remove(email)
}

func (s *Server) GetUser(_ context.Context, email string) *protocol.MemoryUser {
	return s.users.GetUser(email)
}

func (s *Server) GetUsers(_ context.Context) []*protocol.MemoryUser {
	return s.users.GetUsers()
}

func (s *Server) GetUsersCount(_ context.Context) int64 {
	return s.users.GetCount()
}

func (s *Server) Close() error {
	return s.users.Close()
}
