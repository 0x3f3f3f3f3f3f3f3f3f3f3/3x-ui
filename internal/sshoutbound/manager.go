package sshoutbound

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
)

const MaxBridgeConnections = 512

var ErrStaging = errors.New("SSH upstream configuration application in progress")

type bridgeCredentials struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

type managedOutbound struct {
	credentials bridgeCredentials
	connector   *Connector
}

type Manager struct {
	port     int
	secret   [32]byte
	applyMu  sync.Mutex
	mu       sync.Mutex
	listener net.Listener
	current  map[string]*managedOutbound
	routes   map[string]*managedOutbound
	clients  map[net.Conn]*managedOutbound
	workers  sync.WaitGroup
}

type Prepared struct {
	manager *Manager
	desired map[string]*managedOutbound
	created []*managedOutbound
	once    sync.Once
}

func NewManager(port int) (*Manager, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("%w: invalid private bridge port", ErrConfig)
	}
	m := &Manager{port: port, current: make(map[string]*managedOutbound), routes: make(map[string]*managedOutbound), clients: make(map[net.Conn]*managedOutbound)}
	if _, err := rand.Read(m.secret[:]); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) credentials(outbound Outbound) bridgeCredentials {
	raw, _ := json.Marshal(outbound)
	digest := func(label string) string {
		mac := hmac.New(sha256.New, m.secret[:])
		_, _ = mac.Write([]byte(label))
		_, _ = mac.Write(raw)
		return hex.EncodeToString(mac.Sum(nil))
	}
	return bridgeCredentials{User: digest("user")[:32], Pass: digest("password")}
}

func (m *Manager) Render(outbound Outbound) ([]byte, error) {
	if err := validateTag(outbound.Tag); err != nil {
		return nil, err
	}
	if _, _, err := compileConfig(outbound.Settings); err != nil {
		return nil, err
	}
	credentials := m.credentials(outbound)
	return json.Marshal(struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
		Settings struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
			bridgeCredentials
		} `json:"settings"`
	}{Tag: outbound.Tag, Protocol: "socks", Settings: struct {
		Address string `json:"address"`
		Port    int    `json:"port"`
		bridgeCredentials
	}{Address: "127.0.0.1", Port: m.port, bridgeCredentials: credentials}})
}

func (m *Manager) Prepare(desired []Outbound) (*Prepared, error) {
	if !m.applyMu.TryLock() {
		return nil, ErrStaging
	}
	p := &Prepared{manager: m, desired: make(map[string]*managedOutbound)}
	success := false
	defer func() {
		if !success {
			for _, entry := range p.created {
				_ = entry.connector.Close()
			}
			m.applyMu.Unlock()
		}
	}()
	if len(desired) > MaxOutbounds {
		return nil, fmt.Errorf("%w: too many SSH outbounds", ErrConfig)
	}
	for _, outbound := range desired {
		if err := validateTag(outbound.Tag); err != nil {
			return nil, err
		}
		if _, exists := p.desired[outbound.Tag]; exists {
			return nil, fmt.Errorf("%w: duplicate SSH routing tag", ErrConfig)
		}
		credentials := m.credentials(outbound)
		if current := m.current[outbound.Tag]; current != nil && current.credentials == credentials {
			p.desired[outbound.Tag] = current
			continue
		}
		connector, err := NewConnector(outbound.Settings)
		if err != nil {
			return nil, err
		}
		entry := &managedOutbound{credentials: credentials, connector: connector}
		p.created = append(p.created, entry)
		p.desired[outbound.Tag] = entry
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(desired) > 0 && m.listener == nil {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(m.port)))
		if err != nil {
			return nil, fmt.Errorf("SSH upstream private bridge: %w", err)
		}
		m.listener = listener
		m.workers.Go(func() { m.accept(listener) })
	}
	for _, entry := range p.created {
		m.routes[entry.credentials.User] = entry
	}
	success = true
	return p, nil
}

func (p *Prepared) Commit()   { p.once.Do(func() { p.finish(true) }) }
func (p *Prepared) Rollback() { p.once.Do(func() { p.finish(false) }) }

func (p *Prepared) finish(commit bool) {
	m := p.manager
	defer m.applyMu.Unlock()
	m.mu.Lock()
	retired := p.created
	if commit {
		retired = nil
		for tag, entry := range m.current {
			if p.desired[tag] != entry {
				retired = append(retired, entry)
			}
		}
		m.current = p.desired
	}
	m.retireLocked(retired)
	var listener net.Listener
	if len(m.current) == 0 {
		listener = m.listener
		m.listener = nil
		for client := range m.clients {
			_ = client.Close()
		}
	}
	m.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	for _, entry := range retired {
		_ = entry.connector.Close()
	}
	if listener != nil {
		m.workers.Wait()
	}
}

func (m *Manager) retireLocked(entries []*managedOutbound) {
	retired := make(map[*managedOutbound]bool, len(entries))
	for _, entry := range entries {
		delete(m.routes, entry.credentials.User)
		retired[entry] = true
	}
	for client, entry := range m.clients {
		if retired[entry] {
			_ = client.Close()
		}
	}
}

func (m *Manager) Close() error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	listener := m.listener
	m.listener = nil
	current := m.current
	m.current = make(map[string]*managedOutbound)
	m.routes = make(map[string]*managedOutbound)
	for client := range m.clients {
		_ = client.Close()
	}
	m.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	for _, entry := range current {
		_ = entry.connector.Close()
	}
	m.workers.Wait()
	return nil
}

func (m *Manager) accept(listener net.Listener) {
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		m.mu.Lock()
		if m.listener != listener || len(m.clients) >= MaxBridgeConnections {
			m.mu.Unlock()
			_ = client.Close()
			continue
		}
		m.clients[client] = nil
		m.workers.Go(func() {
			defer func() { _ = client.Close(); m.mu.Lock(); delete(m.clients, client); m.mu.Unlock() }()
			m.serve(client)
		})
		m.mu.Unlock()
	}
}

func (m *Manager) authenticate(client net.Conn, user, password string) *Connector {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.routes[user]
	if entry == nil || subtle.ConstantTimeCompare([]byte(password), []byte(entry.credentials.Pass)) != 1 {
		return nil
	}
	if _, present := m.clients[client]; !present {
		return nil
	}
	m.clients[client] = entry
	return entry.connector
}
