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

	"github.com/enfein/mieru/v3/apis/constant"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/enfein/mieru/v3/pkg/common"

	protocol "github.com/mhsanaei/3x-ui/v3/internal/mieru/native"

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
	InboundTag    string
	Bindings      []Binding
	Clients       []Client
	Authenticated func(context.Context, string) error
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
	tag           string
	clients       map[string]*clientGeneration
	users         map[string]*appctlpb.User
	endpoints     []protocol.UnderlayProperties
	controller    *policyflow.Controller
	dial          DialFunc
	authenticated func(context.Context, string) error
	ctx           context.Context
	cancel        context.CancelFunc
	lifecycle     sync.Mutex
	mux           *protocol.Mux
	started       bool
	listeners     ownedListeners
	mu            sync.Mutex
	sessions      map[*managedSession]struct{}
	workers       sync.WaitGroup
}

func New(config Config, controller *policyflow.Controller, dial DialFunc) (*Server, error) {
	if controller == nil || dial == nil || strings.TrimSpace(config.InboundTag) == "" || len(config.Bindings) == 0 || len(config.Bindings) > 16 || len(config.Clients) == 0 {
		return nil, ErrConfig
	}
	clients, err := validateClients(config.Clients)
	if err != nil {
		return nil, err
	}
	s := &Server{tag: config.InboundTag, controller: controller, dial: dial, authenticated: config.Authenticated, sessions: make(map[*managedSession]struct{})}
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
	s.listeners.failed = s.cancel
	s.replaceClients(clients)
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
	if err := s.mux.SetServerLimits(protocol.ServerLimits{Sessions: maxSessions, SessionsPerUser: policyflow.MaxFlows, QueueSegments: 256, QueueBytes: 128 << 10, DisableUserMetrics: true}); err != nil {
		s.cancel()
		_ = s.mux.Close()
		return err
	}
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

func (s *Server) Done() <-chan struct{} {
	return s.ctx.Done()
}

func (s *Server) Close() error {
	s.lifecycle.Lock()
	s.cancel()
	mux := s.mux
	s.lifecycle.Unlock()
	s.listeners.stopAccepting()
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

func (s *Server) handle(conn *managedSession) {
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
	client := s.authenticatedClient(conn)
	if client == nil {
		if conn.transport != nil {
			_ = conn.transport.Close()
		}
		return
	}

	native, ok := conn.Conn.(*protocol.Session)
	if !ok {
		return
	}
	select {
	case <-native.Done():
		return
	default:
	}
	ctx, cancel := context.WithCancel(client.ctx)
	defer cancel()
	s.workers.Go(func() {
		select {
		case <-native.Done():
			cancel()
		case <-ctx.Done():
		}
	})
	if s.authenticated != nil {
		if err := s.authenticated(ctx, client.PolicyID); err != nil {
			return
		}
	}
	d := Destination{PolicyID: client.PolicyID, InboundTag: s.tag, Source: source}
	switch request.Command {
	case constant.Socks5ConnectCmd:
		d.Network = "tcp"
		if !setTarget(&d, request.DstAddr) {
			return
		}
		_ = s.controller.Proxy(ctx, client.PolicyID, conn, func(ctx context.Context) (io.ReadWriteCloser, error) {
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
			if !s.admitSession(conn) {
				_ = target.Close()
				return nil, ErrClosed
			}
			return target, nil
		})
	case constant.Socks5UDPAssociateCmd:
		d.Network = "udp"
		s.serveUDP(ctx, conn, d)
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
