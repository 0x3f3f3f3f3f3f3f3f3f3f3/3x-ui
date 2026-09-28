package sshtunnel

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"

	"golang.org/x/crypto/ssh"
)

type reverseBind struct {
	Address string
	Port    uint32
}

type reverseSession struct {
	server  *Server
	client  Client
	conn    *ssh.ServerConn
	ctx     context.Context
	workers *sync.WaitGroup
	slots   chan struct{}
	mu      sync.Mutex
	closed  bool
	entries map[string]net.Listener
}

func (r *reverseSession) requests(requests <-chan *ssh.Request) {
	for request := range requests {
		var binding reverseBind
		if err := ssh.Unmarshal(request.Payload, &binding); err != nil || binding.Port > 65535 {
			_ = request.Reply(false, nil)
			continue
		}
		address, err := netip.ParseAddr(binding.Address)
		if err != nil || address.Zone() != "" {
			_ = request.Reply(false, nil)
			continue
		}
		address = address.Unmap()
		switch request.Type {
		case "tcpip-forward":
			port, ok := r.listen(address, binding)
			if !ok {
				_ = request.Reply(false, nil)
				continue
			}
			var payload []byte
			if binding.Port == 0 {
				payload = ssh.Marshal(struct{ Port uint32 }{port})
			}
			_ = request.Reply(true, payload)
		case "cancel-tcpip-forward":
			key := net.JoinHostPort(address.String(), strconv.Itoa(int(binding.Port)))
			_ = request.Reply(r.remove(key, nil), nil)
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func (r *reverseSession) listen(address netip.Addr, request reverseBind) (uint32, bool) {
	allowed := false
	for _, rule := range r.client.Reverse {
		if rule.Address == address.String() && uint32(rule.Port) == request.Port {
			allowed = true
			break
		}
	}
	if !allowed {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil || len(r.entries) >= 16 {
		return 0, false
	}
	select {
	case r.server.listeners <- struct{}{}:
	default:
		return 0, false
	}
	network := "tcp4"
	if address.Is6() {
		network = "tcp6"
	}
	listener, err := (&net.ListenConfig{}).Listen(r.ctx, network, net.JoinHostPort(address.String(), strconv.Itoa(int(request.Port))))
	if err != nil {
		<-r.server.listeners
		return 0, false
	}
	key := listener.Addr().String()
	r.entries[key] = listener
	port := uint32(listener.Addr().(*net.TCPAddr).Port)
	r.workers.Go(func() { r.accept(listener, key, request.Address, port) })
	return port, true
}

func (r *reverseSession) remove(key string, expected net.Listener) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	listener := r.entries[key]
	if listener == nil || (expected != nil && listener != expected) {
		return false
	}
	delete(r.entries, key)
	_ = listener.Close()
	<-r.server.listeners
	return true
}

func (r *reverseSession) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for key, listener := range r.entries {
		_ = listener.Close()
		delete(r.entries, key)
		<-r.server.listeners
	}
}

func (r *reverseSession) accept(listener net.Listener, key, address string, port uint32) {
	defer r.remove(key, listener)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		if r.ctx.Err() != nil || !r.server.reserveChannel(r.slots) {
			_ = conn.Close()
			continue
		}
		r.workers.Go(func() {
			defer func() { _ = conn.Close(); <-r.slots; <-r.server.slots }()
			stop := context.AfterFunc(r.ctx, func() { _ = conn.Close() })
			defer stop()
			origin := conn.RemoteAddr().(*net.TCPAddr)
			payload := ssh.Marshal(struct {
				Address    string
				Port       uint32
				Origin     string
				OriginPort uint32
			}{address, port, origin.IP.String(), uint32(origin.Port)})
			_ = r.server.controller.ProxyToClient(r.ctx, r.client.PolicyID, conn, func(context.Context) (io.ReadWriteCloser, error) {
				channel, requests, err := r.conn.OpenChannel("forwarded-tcpip", payload)
				if err != nil {
					return nil, err
				}
				r.workers.Go(func() { ssh.DiscardRequests(requests) })
				return channel, nil
			})
		})
	}
}
