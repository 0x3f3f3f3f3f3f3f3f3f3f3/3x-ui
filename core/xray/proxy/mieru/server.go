package mieru

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	miCommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/constant"
	"github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	miProtocol "github.com/enfein/mieru/v3/pkg/protocol"
	"github.com/enfein/mieru/v3/pkg/protocol/serveruser"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	"google.golang.org/protobuf/proto"
)

type Server struct {
	ctx                        context.Context
	cancel                     context.CancelFunc
	config                     *ServerConfig
	users                      *protocol.PasswordValidator
	mux                        *miProtocol.Mux
	dispatcher                 routing.Dispatcher
	clients                    clientpolicy.Manager
	listen                     xnet.Destination
	tag                        string
	stream                     *internet.MemoryStreamConfig
	mu                         sync.Mutex
	started, listening, closed bool
	connections                map[net.Conn]struct{}
	wg                         sync.WaitGroup
	physicalMu                 sync.Mutex
	physical                   map[string]*underlayConnection
	authMu                     sync.Mutex
	packetUsers                serveruser.Registry
	packetBindings             map[string]*packetBinding
	sniffingRequest            session.SniffingRequest
}

var (
	_ proxy.Inbound     = (*Server)(nil)
	_ proxy.UserManager = (*Server)(nil)
	_ common.Runnable   = (*Server)(nil)
)

func NewServer(ctx context.Context, config *ServerConfig) (*Server, error) {
	inbound := session.InboundFromContext(ctx)
	if inbound == nil || inbound.Source.Port == 0 || !inbound.Source.Address.Family().IsIP() {
		return nil, errors.New("mieru requires a native IP listener and one nonzero port")
	}
	stream, _ := session.StreamSettingsFromContext(ctx).(*internet.MemoryStreamConfig)
	if err := ValidateNativeStream(stream); err != nil {
		return nil, err
	}
	if config.Transport != "TCP" && config.Transport != "UDP" {
		return nil, errors.New("invalid mieru transport")
	}
	users, err := protocol.NewPasswordValidator(nil, nil, nil, 0, nil)
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(core.ToBackgroundDetachedContext(ctx))
	s := &Server{ctx: child, cancel: cancel, config: config, users: users, mux: miProtocol.NewMux(false), listen: inbound.Source, tag: inbound.Tag, stream: stream, connections: make(map[net.Conn]struct{}), physical: make(map[string]*underlayConnection), packetBindings: make(map[string]*packetBinding)}
	if content := session.ContentFromContext(ctx); content != nil {
		s.sniffingRequest = content.SniffingRequest
	}
	for _, entry := range config.Users {
		user, err := entry.ToMemoryUser()
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		if err := s.addUser(user); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	mode := appctlpb.TransportProtocol_TCP
	if config.Transport == "UDP" {
		mode = appctlpb.TransportProtocol_UDP
	}
	mtu := int(config.Mtu)
	if mtu == 0 {
		mtu = 1400
	}
	endpoints, err := appctlcommon.AddrPortToUnderlayProperties(s.listen.Address.IP().String(), []*appctlpb.PortBinding{{Port: proto.Int32(int32(s.listen.Port)), Protocol: mode.Enum()}}, mtu)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	s.mux.SetStreamListenerFactory(s).SetPacketListenerFactory(s).SetServerUserHintIsMandatory(config.UserHintRequired).SetEndpoints(endpoints)
	if err := core.RequireFeatures(ctx, func(dispatcher routing.Dispatcher, clients clientpolicy.Manager) error {
		s.dispatcher, s.clients = dispatcher, clients
		return nil
	}); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Server) libraryUsers() map[string]*appctlpb.User {
	users := make(map[string]*appctlpb.User)
	for _, user := range s.users.GetUsers() {
		account := user.Account.(*Account)
		users[account.Username] = &appctlpb.User{Name: proto.String(account.Username), Password: proto.String(account.Password)}
	}
	return users
}

func (s *Server) addUser(user *protocol.MemoryUser) error {
	account, ok := user.Account.(*Account)
	if !ok || user.ClientID == "" || len(user.ClientID) > 128 || strings.TrimSpace(user.ClientID) != user.ClientID || user.Email == "" || strings.ContainsAny(user.ClientID+user.Email, "\x00\r\n") {
		return errors.New("mieru requires authenticated canonical user identity")
	}
	if err := ValidateCredentials(account.Username, account.Password); err != nil {
		return err
	}
	return s.users.Add(account.Username, account.Password, user)
}

func (s *Server) AddUser(_ context.Context, user *protocol.MemoryUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if err := s.addUser(user); err != nil {
		return err
	}
	s.closeUnderlays("")
	s.mux.SetServerUsers(s.libraryUsers())
	s.packetUsers.SetUsers(s.libraryUsers())
	if s.started && !s.listening {
		return s.startListener()
	}
	return nil
}

func (s *Server) RemoveUser(_ context.Context, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	user := s.users.GetUser(email)
	if user == nil {
		return errors.New("mieru user not found")
	}
	if err := s.users.Remove(email); err != nil {
		return err
	}
	// End cached TCP authentication before this username can be rebound.
	s.closeUnderlays(user.Email)
	s.mux.SetServerUsers(s.libraryUsers())
	s.packetUsers.SetUsers(s.libraryUsers())
	return nil
}

func (s *Server) GetUser(_ context.Context, email string) *protocol.MemoryUser {
	return s.users.GetUser(email)
}
func (s *Server) GetUsers(_ context.Context) []*protocol.MemoryUser { return s.users.GetUsers() }
func (s *Server) GetUsersCount(_ context.Context) int64             { return s.users.GetCount() }
func (*Server) Network() []xnet.Network                             { return nil }
func (*Server) Process(context.Context, xnet.Network, stat.Connection, routing.Dispatcher) error {
	return errors.New("mieru owns its native listener")
}

func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	if s.started {
		return nil
	}
	s.started = true
	s.mux.SetServerUsers(s.libraryUsers())
	s.packetUsers.SetUsers(s.libraryUsers())
	s.packetUsers.SetHintMandatory(s.config.UserHintRequired)
	if s.users.GetCount() == 0 {
		return nil
	}
	return s.startListener()
}

// Caller holds mu; authentication updates and listener startup are serialized.
func (s *Server) startListener() error {
	if err := s.mux.Start(); err != nil {
		return err
	}
	s.listening = true
	s.wg.Add(1)
	go s.accept()
	return nil
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.mux.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		limit := int(s.config.MaxConnections)
		if limit == 0 {
			limit = 1024
		}
		if s.closed || len(s.connections) >= limit {
			s.mu.Unlock()
			_ = conn.Close()
			if s.config.Transport == "UDP" {
				s.releasePacketUser(conn, nil)
			}
			continue
		}
		// UDP's UserContext is populated by the input worker after Accept.
		// Retain candidate bindings now, then resolve only after request Read.
		candidates := s.users.GetUsers()
		s.connections[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn, candidates)
	}
}

func (s *Server) serve(conn net.Conn, candidates []*protocol.MemoryUser) {
	defer s.wg.Done()
	// Release admission before the peer can observe EOF and retry immediately.
	defer func() { s.mu.Lock(); delete(s.connections, conn); s.mu.Unlock(); _ = conn.Close() }()
	if s.config.Transport == "UDP" {
		defer s.releasePacketUser(conn, nil)
	}
	timeout := s.config.HandshakeTimeoutSeconds
	if timeout == 0 {
		timeout = 10
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Duration(timeout) * time.Second))
	handshakeTimer := time.AfterFunc(time.Duration(timeout)*time.Second, func() { _ = conn.Close() })
	defer handshakeTimer.Stop()
	var request model.Request
	if err := request.ReadFromSocks5(conn); err != nil {
		return
	}
	handshakeTimer.Stop()
	_ = conn.SetReadDeadline(time.Time{})
	identity, ok := conn.(miCommon.UserContext)
	if !ok {
		return
	}
	var user *protocol.MemoryUser
	for _, candidate := range candidates {
		account := candidate.Account.(*Account)
		if account.Username == identity.UserName() {
			user = candidate
			break
		}
	}
	if user == nil {
		return
	}
	if s.config.Transport == "UDP" {
		user = s.packetUser(conn)
		if user == nil || user.Account.(*Account).Username != identity.UserName() {
			return
		}
	}
	if s.config.Transport == "TCP" && !s.bindUnderlay(conn, user) {
		return
	}
	untrack, err := user.TrackSession(func() { _ = conn.Close() })
	if err != nil {
		return
	}
	defer untrack()
	lease, err := s.clients.Open(s.ctx, clientpolicy.Metadata{ClientID: user.ClientID, InboundTag: s.tag, AuthenticatedAccount: user.Email}, func() { _ = conn.Close() })
	if err != nil {
		return
	}
	defer lease.Release()
	if request.Command != constant.Socks5ConnectCmd && request.Command != constant.Socks5UDPAssociateCmd {
		_ = model.WriteSocks5Response(conn, constant.Socks5ReplyCommandNotSupported, model.AddrSpec{IP: net.IPv4zero})
		return
	}
	var destination xnet.Destination
	if request.Command == constant.Socks5ConnectCmd {
		destination, err = requestDestination(request.DstAddr, false)
		if err != nil {
			return
		}
	}
	if err := model.WriteSocks5Response(conn, constant.Socks5ReplySuccess, model.AddrSpec{IP: net.IPv4zero}); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	ctx = s.sessionContext(ctx, conn, user)
	if request.Command == constant.Socks5UDPAssociateCmd {
		_ = s.servePackets(ctx, conn, user)
		return
	}
	link := &transport.Link{Reader: &buf.TimeoutWrapperReader{Reader: buf.NewReader(conn)}, Writer: buf.NewWriter(conn)}
	_ = s.dispatcher.DispatchLink(ctx, destination, link)
}

func (s *Server) sessionContext(ctx context.Context, conn net.Conn, user *protocol.MemoryUser) context.Context {
	source := xnet.DestinationFromAddr(conn.RemoteAddr())
	ctx = session.ContextWithInbound(ctx, &session.Inbound{Name: "mieru", Tag: s.tag, Source: source, Local: s.listen, User: user, Conn: conn, CanSpliceCopy: 3})
	ctx = session.ContextWithContent(ctx, &session.Content{SniffingRequest: s.sniffingRequest, PreserveUDPPacketSource: true})
	return session.ContextWithOutbounds(ctx, nil)
}

func requestDestination(address model.AddrSpec, udp bool) (xnet.Destination, error) {
	if address.Port <= 0 || address.Port > 65535 {
		return xnet.Destination{}, errors.New("invalid mieru destination port")
	}
	host := address.FQDN
	if len(address.IP) != 0 {
		host = address.IP.String()
	}
	if host == "" {
		return xnet.Destination{}, errors.New("empty mieru destination")
	}
	if udp {
		return xnet.UDPDestination(xnet.ParseAddress(host), xnet.Port(address.Port)), nil
	}
	return xnet.TCPDestination(xnet.ParseAddress(host), xnet.Port(address.Port)), nil
}

func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	connections := make([]net.Conn, 0, len(s.connections))
	for conn := range s.connections {
		connections = append(connections, conn)
	}
	s.mu.Unlock()
	_ = s.users.Close()
	for _, conn := range connections {
		_ = conn.Close()
	}
	err := s.mux.Close()
	s.wg.Wait()
	s.authMu.Lock()
	for key, binding := range s.packetBindings {
		binding.timer.Stop()
		delete(s.packetBindings, key)
	}
	s.authMu.Unlock()
	return err
}

func (s *Server) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	addr, err := net.ResolveTCPAddr(network, address)
	if err != nil {
		return nil, err
	}
	var socket *internet.SocketConfig
	if s.stream != nil {
		socket = s.stream.SocketSettings
	}
	listener, err := internet.ListenSystem(ctx, addr, socket)
	if err != nil {
		return nil, err
	}
	return &underlayListener{Listener: listener, server: s}, nil
}

func (s *Server) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	addr, err := net.ResolveUDPAddr(network, address)
	if err != nil {
		return nil, err
	}
	var socket *internet.SocketConfig
	if s.stream != nil {
		socket = s.stream.SocketSettings
	}
	conn, err := internet.ListenSystemPacket(ctx, addr, socket)
	if err != nil {
		return nil, err
	}
	return &authenticatedPacketConn{PacketConn: conn, server: s}, nil
}

func init() {
	common.Must(common.RegisterConfig((*ServerConfig)(nil), func(ctx context.Context, raw interface{}) (interface{}, error) {
		server, err := NewServer(ctx, raw.(*ServerConfig))
		if err != nil {
			return nil, fmt.Errorf("native mieru inbound: %w", err)
		}
		return server, nil
	}))
}
