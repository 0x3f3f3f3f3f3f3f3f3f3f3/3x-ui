package ssh

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport/internet/stat"
	gossh "golang.org/x/crypto/ssh"
)

type Server struct {
	limits        limits
	host          gossh.Signer
	allowPassword bool
	users         userStore
	clients       clientpolicy.Manager
	mu            sync.Mutex
	closed        bool
	connections   map[*serverTransport]struct{}
	perClient     map[string]int
	channels      int
	reverse       reversePolicy
}

type serverTransport struct {
	server       *Server
	raw          stat.Connection
	conn         *gossh.ServerConn
	ctx          context.Context
	cancel       context.CancelFunc
	user         *protocol.MemoryUser
	activity     atomic.Int64
	mu           sync.Mutex
	closed       bool
	channels     map[*channelConn]struct{}
	channelSlots int
	forwards     map[string]*reverseListener
}

func NewServer(ctx context.Context, c *ServerConfig) (*Server, error) {
	l, err := serverLimits(c)
	if err != nil {
		return nil, err
	}
	host, err := privateKeyFile(c.HostKeyFile)
	if err != nil {
		return nil, err
	}
	s := &Server{limits: l, host: host, allowPassword: c.AllowPassword, connections: make(map[*serverTransport]struct{}), perClient: make(map[string]int), users: userStore{byUsername: make(map[string]*userEntry), byEmail: make(map[string]*userEntry)}}
	s.reverse, err = buildReversePolicy(c.Reverse)
	if err != nil {
		return nil, err
	}
	core.OptionalFeatures(ctx, func(manager clientpolicy.Manager) { s.clients = manager })
	for _, p := range c.Users {
		if p == nil {
			return nil, ErrConfiguration
		}
		user, err := p.ToMemoryUser()
		if err != nil {
			return nil, err
		}
		if err := s.AddUser(ctx, user); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (*Server) Network() []net.Network { return []net.Network{net.Network_TCP} }

func (t *serverTransport) close() {
	t.server.mu.Lock()
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		t.server.mu.Unlock()
		return
	}
	t.closed = true
	delete(t.server.connections, t)
	if t.user != nil {
		t.server.perClient[t.user.ClientID]--
	}
	channels := make([]*channelConn, 0, len(t.channels))
	for c := range t.channels {
		channels = append(channels, c)
	}
	listeners := make([]*reverseListener, 0, len(t.forwards))
	for _, r := range t.forwards {
		listeners = append(listeners, r)
	}
	t.forwards = nil
	t.mu.Unlock()
	t.server.mu.Unlock()
	t.cancel()
	t.raw.Close()
	for _, c := range channels {
		c.Close()
	}
	for _, r := range listeners {
		r.close()
	}
}

func (t *serverTransport) idleLoop() {
	interval := min(t.server.limits.idle/4, time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, t.activity.Load())) >= t.server.limits.idle {
				t.close()
				return
			}
		}
	}
}

func (s *Server) Process(ctx context.Context, network net.Network, raw stat.Connection, dispatcher routing.Dispatcher) error {
	if network != net.Network_TCP {
		return ErrConfiguration
	}
	child, cancel := context.WithCancel(ctx)
	t := &serverTransport{server: s, raw: raw, ctx: child, cancel: cancel, channels: make(map[*channelConn]struct{}), forwards: make(map[string]*reverseListener)}
	s.mu.Lock()
	if s.closed || len(s.connections) >= s.limits.connections {
		s.mu.Unlock()
		cancel()
		return ErrResourceLimit
	}
	s.connections[t] = struct{}{}
	s.mu.Unlock()
	defer t.close()
	stop := context.AfterFunc(child, t.close)
	defer stop()
	if err := raw.SetDeadline(time.Now().Add(s.limits.handshake)); err != nil {
		return err
	}
	var lease *clientpolicy.Session
	var untrack func()
	offers := make(map[*gossh.Permissions]*protocol.MemoryUser)
	defer func() {
		if lease != nil {
			lease.Release()
		}
		if untrack != nil {
			untrack()
		}
	}()
	authenticate := func(user *protocol.MemoryUser) (*gossh.Permissions, error) {
		if user == nil || s.clients == nil {
			return nil, ErrAuthentication
		}
		if t.user != nil {
			return nil, ErrAuthentication
		}
		s.mu.Lock()
		t.mu.Lock()
		if s.closed || t.closed || s.perClient[user.ClientID] >= s.limits.userConnections {
			t.mu.Unlock()
			s.mu.Unlock()
			return nil, ErrResourceLimit
		}
		s.perClient[user.ClientID]++
		t.user = user
		t.mu.Unlock()
		s.mu.Unlock()
		var err error
		untrack, err = user.TrackSession(t.close)
		if err != nil {
			return nil, err
		}
		lease, err = s.clients.Open(child, clientpolicy.Metadata{ClientID: user.ClientID, InboundTag: inboundTag(ctx), AuthenticatedAccount: user.Email, OriginalTarget: "ssh-transport", ActualTarget: "ssh-transport"}, t.close)
		if err != nil {
			return nil, err
		}
		return &gossh.Permissions{Extensions: map[string]string{"client_id": user.ClientID}}, nil
	}
	cfg := &gossh.ServerConfig{
		MaxAuthTries: s.limits.authTries,
		PublicKeyCallback: func(m gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			user := s.keyUser(m.User(), key)
			if user == nil {
				return nil, ErrAuthentication
			}
			if len(offers) >= 64 {
				return nil, ErrResourceLimit
			}
			permissions := &gossh.Permissions{}
			offers[permissions] = user
			return permissions, nil
		},
		VerifiedPublicKeyCallback: func(_ gossh.ConnMetadata, _ gossh.PublicKey, permissions *gossh.Permissions, _ string) (*gossh.Permissions, error) {
			return authenticate(offers[permissions])
		},
	}
	if s.allowPassword {
		cfg.PasswordCallback = func(m gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			return authenticate(s.passwordUser(m.User(), password))
		}
	}
	cfg.AddHostKey(s.host)
	conn, channels, requests, err := gossh.NewServerConn(raw, cfg)
	if err != nil {
		return err
	}
	t.conn = conn
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return err
	}
	t.activity.Store(time.Now().UnixNano())
	go t.idleLoop()
	go func() {
		for request := range requests {
			t.globalRequest(ctx, request)
		}
	}()
	for {
		select {
		case <-child.Done():
			return child.Err()
		case incoming, ok := <-channels:
			if !ok {
				return conn.Wait()
			}
			s.directChannel(ctx, t, incoming, dispatcher)
		}
	}
}

func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	connections := make([]*serverTransport, 0, len(s.connections))
	for c := range s.connections {
		connections = append(connections, c)
	}
	s.mu.Unlock()
	s.users.mu.Lock()
	s.users.closed = true
	users := make([]*protocol.MemoryUser, 0, len(s.users.byEmail))
	for _, e := range s.users.byEmail {
		users = append(users, e.user)
	}
	s.users.mu.Unlock()
	for _, u := range users {
		u.RevokeCredential()
	}
	for _, c := range connections {
		c.close()
	}
	return nil
}

func init() {
	common.Must(common.RegisterConfig((*ServerConfig)(nil), func(ctx context.Context, raw interface{}) (interface{}, error) {
		return NewServer(ctx, raw.(*ServerConfig))
	}))
}
