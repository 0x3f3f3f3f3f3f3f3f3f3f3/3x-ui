// Package sshtunnel provides a dedicated SSH service with forwarding-only permissions.
package sshtunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
)

var (
	ErrConfig = errors.New("invalid SSH tunnel configuration")
	ErrAuth   = errors.New("SSH tunnel authentication failed")
	ErrClosed = errors.New("SSH tunnel server is closed")
)

type Destination struct {
	PolicyID   string
	InboundTag string
	Host       string
	Port       uint16
	Source     netip.AddrPort
}

type DialFunc func(context.Context, Destination) (io.ReadWriteCloser, error)

// Empty rules deny all targets; '*' and port zero explicitly allow any host or port.
type TargetRule struct {
	Host string
	Port uint16
}

// Reverse rules authorize exact bind addresses/ports; port zero permits only OS-allocated ports.
type ReverseRule struct {
	Address string
	Port    uint16
}

type Client struct {
	PolicyID   string
	Username   string
	PublicKeys []ssh.PublicKey
	Targets    []TargetRule
	Reverse    []ReverseRule
}

type Config struct {
	InboundTag    string
	HostKey       ssh.Signer
	Clients       []Client
	Authenticated func(context.Context, string) error
}

type Server struct {
	tag           string
	sshConfig     *ssh.ServerConfig
	clients       map[string]Client
	sessions      map[net.Conn]authenticatedSession
	controller    *policyflow.Controller
	dial          DialFunc
	authenticated func(context.Context, string) error
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	listener      net.Listener
	started       bool
	serving       bool
	conns         map[net.Conn]struct{}
	slots         chan struct{}
	listeners     chan struct{}
	workers       sync.WaitGroup
}

func NewServer(config Config, controller *policyflow.Controller, dial DialFunc) (*Server, error) {
	if config.InboundTag == "" || config.HostKey == nil || controller == nil || dial == nil {
		return nil, ErrConfig
	}
	clients, err := validateClients(config.Clients)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{tag: config.InboundTag, clients: clients, controller: controller, dial: dial, ctx: ctx, cancel: cancel, conns: make(map[net.Conn]struct{}), slots: make(chan struct{}, 256), listeners: make(chan struct{}, 256)}
	s.authenticated = config.Authenticated
	s.sessions = make(map[net.Conn]authenticatedSession)
	s.sshConfig = &ssh.ServerConfig{MaxAuthTries: 3, PublicKeyCallback: s.authenticate, ServerVersion: "SSH-2.0-3x-ui-tunnel"}
	s.sshConfig.AddHostKey(config.HostKey)
	return s, nil
}

func (s *Server) authenticate(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[conn.User()]
	if !ok {
		return nil, ErrAuth
	}
	for _, allowed := range client.PublicKeys {
		if bytes.Equal(allowed.Marshal(), key.Marshal()) {
			return &ssh.Permissions{Extensions: map[string]string{"policy-id": client.PolicyID, "auth-key": ssh.FingerprintSHA256(key)}}, nil
		}
	}
	return nil, ErrAuth
}

func (s *Server) Serve(listener net.Listener) error {
	s.mu.Lock()
	if s.started || s.ctx.Err() != nil {
		s.mu.Unlock()
		return ErrClosed
	}
	s.started, s.serving, s.listener = true, true, listener
	s.workers.Add(1)
	s.mu.Unlock()
	defer s.workers.Done()
	defer func() {
		s.mu.Lock()
		s.serving = false
		s.mu.Unlock()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.mu.Lock()
		if len(s.conns) >= 256 || s.ctx.Err() != nil {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.workers.Go(func() {
			defer func() {
				_ = conn.Close()
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
			}()
			s.handle(conn)
		})
	}
}

func (s *Server) Close() {
	s.cancel()
	s.mu.Lock()
	if s.listener != nil {
		_ = s.listener.Close()
	}
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.workers.Wait()
}

func (s *Server) handle(raw net.Conn) {
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	conn, channels, requests, err := ssh.NewServerConn(raw, s.sshConfig)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = raw.SetDeadline(time.Time{})
	client, sessionCtx, releaseSession, ok := s.registerSession(raw, conn)
	if !ok {
		return
	}
	defer releaseSession()
	if s.authenticated != nil {
		if err := s.authenticated(sessionCtx, client.PolicyID); err != nil {
			return
		}
	}
	transport, err := s.controller.Open(sessionCtx, client.PolicyID, raw)
	if err != nil {
		return
	}
	defer transport.Close()
	s.admitSession(raw)
	ctx, cancel := context.WithCancel(transport.Context())
	var workers sync.WaitGroup
	localSlots := make(chan struct{}, 64)
	reverse := &reverseSession{server: s, client: client, conn: conn, ctx: ctx, workers: &workers, slots: localSlots, entries: make(map[string]net.Listener)}
	defer func() { cancel(); reverse.close(); _ = conn.Close(); workers.Wait() }()
	workers.Go(func() { reverse.requests(requests) })
	for incoming := range channels {
		if incoming.ChannelType() != "direct-tcpip" {
			_ = incoming.Reject(ssh.Prohibited, "only TCP forwarding is permitted")
			continue
		}
		var request struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(incoming.ExtraData(), &request); err != nil || request.Port == 0 || request.Port > 65535 || !targetAllowed(client.Targets, request.Host, uint16(request.Port)) {
			_ = incoming.Reject(ssh.Prohibited, "target is not permitted")
			continue
		}
		if !s.reserveChannel(localSlots) {
			_ = incoming.Reject(ssh.ResourceShortage, "forwarding channel capacity reached")
			continue
		}
		release := func() { <-localSlots; <-s.slots }
		channel, channelRequests, err := incoming.Accept()
		if err != nil {
			release()
			continue
		}
		workers.Go(func() { ssh.DiscardRequests(channelRequests) })
		workers.Go(func() {
			defer release()
			_ = s.controller.Proxy(ctx, client.PolicyID, channel, func(ctx context.Context) (io.ReadWriteCloser, error) {
				source, ok := raw.RemoteAddr().(*net.TCPAddr)
				if !ok {
					return nil, ErrConfig
				}
				return s.dial(ctx, Destination{PolicyID: client.PolicyID, InboundTag: s.tag, Host: request.Host, Port: uint16(request.Port), Source: source.AddrPort()})
			})
		})
	}
}

func (s *Server) reserveChannel(local chan struct{}) bool {
	select {
	case local <- struct{}{}:
	default:
		return false
	}
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		<-local
		return false
	}
}

func targetAllowed(rules []TargetRule, host string, port uint16) bool {
	canonical, err := canonicalHost(host)
	if err != nil {
		return false
	}
	for _, rule := range rules {
		if (rule.Host == "*" || rule.Host == canonical) && (rule.Port == 0 || rule.Port == port) {
			return true
		}
	}
	return false
}

func canonicalHost(host string) (string, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		if address.Zone() != "" {
			return "", ErrConfig
		}
		return address.Unmap().String(), nil
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(host) == 0 || len(host) > 253 {
		return "", ErrConfig
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrConfig
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", ErrConfig
			}
		}
	}
	return host, nil
}
