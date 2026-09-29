package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/mieru"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/routedbridge"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var mieruRuntimeState struct {
	sync.Mutex
	manager    *mieruRuntimeManager
	compiledDB *gorm.DB
	compiled   map[int]*mieruRuntimeEntry
}

type mieruRuntimeManager struct {
	mu       sync.Mutex
	db       *gorm.DB
	policy   *managedPolicyLease
	entries  map[int]*mieruRuntimeEntry
	expected [32]byte
	pending  [32]byte
	cancel   context.CancelFunc
	done     chan struct{}
}

type mieruRuntimeEntry struct {
	inbound     *model.Inbound
	fingerprint [32]byte
	bridge      *routedbridge.ManagedBridge
	config      mieru.Config
	server      *mieru.Server
	suspended   bool
	lastError   string
}

type mieruRuntimePlan struct {
	entries map[int]*mieruRuntimeEntry
	configs map[int]mieru.Config
}

func managedMieruRuntime() *mieruRuntimeManager {
	mieruRuntimeState.Lock()
	defer mieruRuntimeState.Unlock()
	if mieruRuntimeState.manager != nil && mieruRuntimeState.manager.db != database.GetDB() {
		mieruRuntimeState.manager.close()
		mieruRuntimeState.manager = nil
	}
	if mieruRuntimeState.manager == nil {
		ctx, cancel := context.WithCancel(context.Background())
		db := database.GetDB()
		m := &mieruRuntimeManager{db: db, policy: acquireManagedPolicy(db), entries: make(map[int]*mieruRuntimeEntry), cancel: cancel, done: make(chan struct{})}
		mieruRuntimeState.manager = m
		go m.watch(ctx)
	}
	return mieruRuntimeState.manager
}

func stopManagedMieru() {
	mieruRuntimeState.Lock()
	defer mieruRuntimeState.Unlock()
	if m := mieruRuntimeState.manager; m != nil {
		m.close()
		mieruRuntimeState.manager = nil
	}
}

func (m *mieruRuntimeManager) close() {
	m.cancel()
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.entries {
		entry.stop()
	}
	m.policy.Close()
}

func (entry *mieruRuntimeEntry) stop() {
	if entry.server != nil {
		_ = entry.server.Close()
		entry.server = nil
	}
}

func (entry *mieruRuntimeEntry) report(reason string) {
	if reason == entry.lastError {
		return
	}
	entry.lastError = reason
	if reason != "" {
		logger.Warningf("managed mieru inbound %d protected: %s", entry.inbound.Id, reason)
	}
}

func mieruRuntimeClients(db *gorm.DB, inboundID int) ([]mieru.Client, []routedbridge.ClientBinding, error) {
	var records []model.ClientRecord
	if err := db.Model(&model.ClientRecord{}).Joins("JOIN client_inbounds ci ON ci.client_id = clients.id").Where("ci.inbound_id = ?", inboundID).Order("clients.id").Find(&records).Error; err != nil {
		return nil, nil, err
	}
	var clients []mieru.Client
	var bindings []routedbridge.ClientBinding
	for _, record := range records {
		bindings = append(bindings, routedbridge.ClientBinding{PolicyID: record.PolicyID, Email: record.Email})
		if !record.Enable {
			continue
		}
		client, err := mieruClientBinding(*record.ToClient(), record.PolicyID)
		if err != nil {
			return nil, nil, err
		}
		clients = append(clients, client)
	}
	return clients, bindings, nil
}

func buildManagedMieru(cfg *xray.Config, inbounds []*model.Inbound) (*mieruRuntimePlan, error) {
	db := database.GetDB()
	plan := &mieruRuntimePlan{entries: make(map[int]*mieruRuntimeEntry), configs: make(map[int]mieru.Config)}
	for _, inbound := range inbounds {
		if inbound.Protocol != model.Mieru || !inbound.Enable || inbound.NodeID != nil {
			continue
		}
		var settings mieruInboundSettings
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			return nil, errors.New("invalid stored mieru settings")
		}
		clients, bindings, err := mieruRuntimeClients(db, inbound.Id)
		if err != nil {
			return nil, err
		}
		if len(clients) == 0 {
			continue
		}
		entry, err := compiledMieruEntry(db, inbound, settings, bindings)
		if err != nil {
			return nil, err
		}
		if err := entry.bridge.Apply(cfg); err != nil {
			return nil, err
		}
		host := strings.Trim(inbound.Listen, "[]")
		if host == "" {
			host = "0.0.0.0"
		}
		address := net.JoinHostPort(host, strconv.Itoa(inbound.Port))
		var listeners []mieru.Binding
		switch settings.Network {
		case "tcp", "udp":
			listeners = []mieru.Binding{{Network: settings.Network, Address: address}}
		case "both":
			listeners = []mieru.Binding{{Network: "tcp", Address: address}, {Network: "udp", Address: address}}
		default:
			return nil, errors.New("invalid stored mieru transport")
		}
		plan.entries[inbound.Id] = entry
		plan.configs[inbound.Id] = mieru.Config{InboundTag: inbound.Tag, Bindings: listeners, Clients: clients, Authenticated: startManagedClient}
	}
	mieruRuntimeState.Lock()
	if mieruRuntimeState.compiledDB == db {
		for id := range mieruRuntimeState.compiled {
			if plan.entries[id] == nil {
				delete(mieruRuntimeState.compiled, id)
			}
		}
	}
	mieruRuntimeState.Unlock()
	return plan, nil
}

func compiledMieruEntry(db *gorm.DB, inbound *model.Inbound, settings mieruInboundSettings, bindings []routedbridge.ClientBinding) (*mieruRuntimeEntry, error) {
	fingerprint := managedConfigFingerprint([]any{inbound.Tag, inbound.Listen, inbound.Port, settings.Network, settings.BridgePort, bindings})
	mieruRuntimeState.Lock()
	defer mieruRuntimeState.Unlock()
	if m := mieruRuntimeState.manager; m != nil && m.db == db {
		m.mu.Lock()
		entry := m.entries[inbound.Id]
		m.mu.Unlock()
		if entry != nil && entry.fingerprint == fingerprint {
			return entry, nil
		}
	}
	if mieruRuntimeState.compiledDB != db {
		mieruRuntimeState.compiledDB = db
		mieruRuntimeState.compiled = make(map[int]*mieruRuntimeEntry)
	}
	if entry := mieruRuntimeState.compiled[inbound.Id]; entry != nil && entry.fingerprint == fingerprint {
		return entry, nil
	}
	address, err := netip.ParseAddrPort(net.JoinHostPort("127.0.0.1", strconv.Itoa(settings.BridgePort)))
	if err != nil {
		return nil, errors.New("invalid stored mieru bridge port")
	}
	bridge, err := routedbridge.NewManaged(inbound.Tag, address, bindings)
	if err != nil {
		return nil, err
	}
	entry := &mieruRuntimeEntry{inbound: inbound, fingerprint: fingerprint, bridge: bridge}
	mieruRuntimeState.compiled[inbound.Id] = entry
	return entry, nil
}

func stageMieruRuntime(cfg *xray.Config) func() {
	mieruRuntimeState.Lock()
	m := mieruRuntimeState.manager
	mieruRuntimeState.Unlock()
	if m == nil {
		return func() {}
	}
	fingerprint := managedConfigFingerprint(cfg)
	m.mu.Lock()
	m.pending = fingerprint
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		if m.pending == fingerprint {
			m.pending = [32]byte{}
		}
		m.mu.Unlock()
	}
}

func (plan *mieruRuntimePlan) apply(cfg *xray.Config) {
	if len(plan.entries) == 0 {
		stopManagedMieru()
		return
	}
	m := managedMieruRuntime()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, entry := range m.entries {
		if plan.entries[id] != entry {
			entry.stop()
		}
	}
	m.entries = plan.entries
	for id, entry := range plan.entries {
		entry.config = plan.configs[id]
		entry.suspended = false
	}
	m.expected, m.pending = managedConfigFingerprint(cfg), [32]byte{}
}

func (m *mieruRuntimeManager) watch(ctx context.Context) {
	defer close(m.done)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

func (m *mieruRuntimeManager) reconcile(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	process := currentXrayProcess()
	matched := false
	if process != nil && process.IsRunning() {
		fingerprint := managedConfigFingerprint(process.GetConfig())
		matched = fingerprint == m.expected || fingerprint == m.pending
	}
	if !matched {
		for _, entry := range m.entries {
			entry.stop()
			entry.report("waiting for the applied Xray configuration")
		}
		return
	}
	var ids []string
	for _, entry := range m.entries {
		if !entry.suspended {
			for _, client := range entry.config.Clients {
				ids = append(ids, client.PolicyID)
			}
		}
	}
	policies, err := loadManagedClientPolicies(ctx, m.db, ids)
	if err != nil {
		for _, entry := range m.entries {
			entry.stop()
			entry.report("client rate policy unavailable")
		}
		return
	}
	configured := make(map[string]error)
	for _, entry := range m.entries {
		if entry.suspended {
			continue
		}
		ready := true
		for _, client := range entry.config.Clients {
			err, exists := configured[client.PolicyID]
			if !exists {
				policy := policies[client.PolicyID]
				operation, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				err = m.policy.controller.Configure(operation, client.PolicyID, policyflow.Rates{Upload: policy.UploadBps, Download: policy.DownloadBps})
				cancel()
				configured[client.PolicyID] = err
			}
			if err != nil {
				ready = false
				break
			}
		}
		if !ready {
			entry.stop()
			entry.report("client accounting policy unavailable")
			continue
		}
		if entry.server != nil {
			select {
			case <-entry.server.Done():
				entry.stop()
				entry.report("listener stopped")
				continue
			default:
			}
			if err := entry.server.UpdateClients(entry.config.Clients); err != nil {
				entry.stop()
				entry.report("client credentials could not be applied")
			}
			continue
		}
		if err := entry.bridge.Check(ctx); err != nil {
			entry.report("authenticated routing bridge unavailable")
			continue
		}
		bridge := entry.bridge
		server, err := mieru.New(entry.config, m.policy.controller, func(ctx context.Context, dest mieru.Destination) (net.Conn, error) {
			if dest.Network == "udp" {
				return bridge.DialUDP(ctx, dest.PolicyID, dest.Source, dest.Host, dest.Port)
			}
			return bridge.DialTCP(ctx, dest.PolicyID, dest.Source, dest.Host, dest.Port)
		})
		if err != nil {
			entry.report("invalid server configuration")
			continue
		}
		if err := server.Start(); err != nil {
			_ = server.Close()
			entry.report("listener unavailable")
			continue
		}
		entry.server = server
		entry.report("")
	}
}

func NotifyMieruChange(inboundID int, full bool) error {
	mieruRuntimeState.Lock()
	m := mieruRuntimeState.manager
	mieruRuntimeState.Unlock()
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[inboundID]
	if entry == nil {
		return nil
	}
	if full {
		entry.suspended = true
		entry.stop()
		return nil
	}
	clients, _, err := mieruRuntimeClients(m.db, inboundID)
	if err != nil {
		entry.stop()
		return err
	}
	ready := clients[:0]
	for _, client := range clients {
		if entry.bridge.MatchesBinding(client.PolicyID, client.Username) {
			ready = append(ready, client)
		}
	}
	entry.config.Clients = ready
	if entry.server != nil {
		return entry.server.UpdateClients(ready)
	}
	return nil
}
