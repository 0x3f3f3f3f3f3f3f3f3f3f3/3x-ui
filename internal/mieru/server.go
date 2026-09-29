// Package mieru adapts the official mieru wire engine to shared client policy and explicit routing.
package mieru

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/constant"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/enfein/mieru/v3/pkg/common"
	"github.com/enfein/mieru/v3/pkg/protocol"

	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
)

const (
	Version          = "3.38.0"
	maxSessions      = 256
	handshakeTimeout = 5 * time.Second
	dialTimeout      = 10 * time.Second
)

var (
	ErrConfig  = errors.New("invalid mieru configuration")
	ErrClosed  = errors.New("mieru server is closed")
	ErrStarted = errors.New("mieru server already started")
)

type Client struct {
	PolicyID string
	Username string
	Password string
}

type Binding struct{ Network, Address string }

type Config struct {
	InboundTag string
	Bindings   []Binding
	Clients    []Client
}

type Destination struct {
	PolicyID   string
	InboundTag string
	Network    string
	Host       string
	Port       uint16
	Source     netip.AddrPort
}

// UDP connectors preserve datagrams and expose actual IP peers through ReadFrom or a connected RemoteAddr.
type DialFunc func(context.Context, Destination) (net.Conn, error)

type Server struct {
	tag        string
	clients    map[string]Client
	users      map[string]*appctlpb.User
	endpoints  []protocol.UnderlayProperties
	controller *policyflow.Controller
	dial       DialFunc
	ctx        context.Context
	cancel     context.CancelFunc
	lifecycle  sync.Mutex
	mux        *protocol.Mux
	started    bool
	listeners  ownedListeners
	mu         sync.Mutex
	sessions   map[*managedSession]struct{}
	workers    sync.WaitGroup
}

func New(config Config, controller *policyflow.Controller, dial DialFunc) (*Server, error) {
	if controller == nil || dial == nil || strings.TrimSpace(config.InboundTag) == "" || len(config.Bindings) == 0 || len(config.Bindings) > 16 || len(config.Clients) == 0 {
		return nil, ErrConfig
	}
	s := &Server{tag: config.InboundTag, controller: controller, dial: dial, clients: make(map[string]Client), users: make(map[string]*appctlpb.User), sessions: make(map[*managedSession]struct{})}
	for _, client := range config.Clients {
		user := &appctlpb.User{Name: &client.Username, Password: &client.Password}
		if client.PolicyID == "" || strings.TrimSpace(client.Username) != client.Username || appctlcommon.ValidateServerConfigSingleUser(user) != nil {
			return nil, ErrConfig
		}
		if _, found := s.clients[client.Username]; found {
			return nil, ErrConfig
		}
		s.clients[client.Username], s.users[client.Username] = client, user
	}
	seen := make(map[Binding]bool)
	for _, binding := range config.Bindings {
		address, err := netip.ParseAddrPort(binding.Address)
		if err != nil || seen[binding] {
			return nil, ErrConfig
		}
		seen[binding] = true
		var endpoint protocol.UnderlayProperties
		switch binding.Network {
		case "tcp":
			endpoint = protocol.NewUnderlayProperties(common.DefaultMTU, common.StreamTransport, net.TCPAddrFromAddrPort(address), nil)
		case "udp":
			endpoint = protocol.NewUnderlayProperties(common.DefaultMTU, common.PacketTransport, net.UDPAddrFromAddrPort(address), nil)
		default:
			return nil, ErrConfig
		}
		s.endpoints = append(s.endpoints, endpoint)
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s, nil
}

func (s *Server) Start() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.ctx.Err() != nil {
		return ErrClosed
	}
	if s.started {
		return ErrStarted
	}
	s.started = true
	s.mux = protocol.NewMux(false)
	s.mux.SetStreamListenerFactory(&s.listeners).SetPacketListenerFactory(&s.listeners).
		SetServerUsers(s.users).SetEndpoints(s.endpoints)
	if err := s.mux.Start(); err != nil {
		s.cancel()
		s.listeners.close()
		_ = s.mux.Close()
		return fmt.Errorf("start mieru listener: %w", err)
	}
	s.workers.Go(s.serve)
	return nil
}

func (s *Server) Addresses() []net.Addr {
	s.listeners.mu.Lock()
	defer s.listeners.mu.Unlock()
	return append([]net.Addr(nil), s.listeners.addresses...)
}

func (s *Server) Close() error {
	s.lifecycle.Lock()
	s.cancel()
	mux := s.mux
	s.lifecycle.Unlock()
	s.listeners.closeStreams()
	for _, conn := range s.closeSessions() {
		<-conn.done
	}
	s.listeners.close()
	if mux != nil {
		_ = mux.Close()
	}
	s.workers.Wait()
	return nil
}

func (s *Server) closeSessions() []*managedSession {
	s.mu.Lock()
	conns := make([]*managedSession, 0, len(s.sessions))
	for conn := range s.sessions {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	return conns
}

func (s *Server) serve() {
	defer s.cancel()
	defer s.closeSessions()
	for {
		conn, err := s.mux.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.ctx.Err() != nil || len(s.sessions) >= maxSessions {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		managed := &managedSession{Conn: conn, server: s, transport: s.listeners.transportFor(conn), done: make(chan struct{})}
		s.sessions[managed] = struct{}{}
		s.mu.Unlock()
		s.workers.Go(func() {
			defer managed.Close()
			s.handle(managed)
		})
	}
}

func (s *Server) handle(conn net.Conn) {
	identity, ok := conn.(apicommon.UserContext)
	if !ok {
		return
	}
	source, err := netip.ParseAddrPort(conn.RemoteAddr().String())
	if err != nil {
		return
	}
	source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
	var request apimodel.Request
	reader := handshakeReader{conn: conn, deadline: time.Now().Add(handshakeTimeout)}
	if err := request.ReadFromSocks5(reader); err != nil || request.Raw[2] != 0 {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	// Native Accept can precede the segment worker that publishes the authenticated user.
	client, ok := s.clients[identity.UserName()]
	if !ok {
		return
	}
	d := Destination{PolicyID: client.PolicyID, InboundTag: s.tag, Source: source}
	switch request.Command {
	case constant.Socks5ConnectCmd:
		d.Network = "tcp"
		if !setTarget(&d, request.DstAddr) {
			return
		}
		_ = s.controller.Proxy(s.ctx, client.PolicyID, conn, func(ctx context.Context) (io.ReadWriteCloser, error) {
			ctx, cancel := context.WithTimeout(ctx, dialTimeout)
			defer cancel()
			target, err := s.dial(ctx, d)
			if err != nil {
				return nil, err
			}
			if err := writeReply(conn); err != nil {
				_ = target.Close()
				return nil, err
			}
			return target, nil
		})
	case constant.Socks5UDPAssociateCmd:
		d.Network = "udp"
		s.serveUDP(conn, d)
	}
}

type handshakeReader struct {
	conn     net.Conn
	deadline time.Time
}

func (r handshakeReader) Read(p []byte) (int, error) {
	if !time.Now().Before(r.deadline) {
		return 0, context.DeadlineExceeded
	}
	// Native Read clears its deadline; every partial read must retain the same total limit.
	if err := r.conn.SetReadDeadline(r.deadline); err != nil {
		return 0, err
	}
	return r.conn.Read(p)
}

func setTarget(d *Destination, address apimodel.AddrSpec) bool {
	d.Host = address.FQDN
	if len(address.IP) != 0 {
		d.Host = address.IP.String()
	}
	if d.Host == "" || strings.ContainsAny(d.Host, "\x00\r\n\t /\\") || address.Port < 1 || address.Port > 65535 {
		return false
	}
	d.Port = uint16(address.Port)
	return true
}

func writeReply(conn net.Conn) error {
	_ = conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	return apimodel.WriteSocks5Response(conn, 0, apimodel.AddrSpec{IP: net.IPv4zero})
}
