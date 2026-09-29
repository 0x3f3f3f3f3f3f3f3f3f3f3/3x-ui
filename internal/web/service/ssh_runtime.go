package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/routedbridge"
	"github.com/mhsanaei/3x-ui/v3/internal/sshoutbound"
	"github.com/mhsanaei/3x-ui/v3/internal/sshtunnel"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var sshRuntimeState struct {
	sync.Mutex
	manager    *sshRuntimeManager
	compiledDB *gorm.DB
	compiled   map[int]*sshRuntimeEntry
}

type sshRuntimeManager struct {
	mu         sync.Mutex
	db         *gorm.DB
	policy     *managedPolicyLease
	controller *policyflow.Controller
	entries    map[int]*sshRuntimeEntry
	expected   [32]byte
	pending    [32]byte
	cancel     context.CancelFunc
	done       chan struct{}
}

type sshRuntimeEntry struct {
	inbound     *model.Inbound
	fingerprint [32]byte
	bridge      *routedbridge.Bridge
	config      sshtunnel.Config
	server      *sshtunnel.Server
	suspended   bool
	lastError   string
	serveDone   <-chan error
}

func sshConfigFingerprint(v any) [32]byte {
	data, _ := json.Marshal(v)
	return sha256.Sum256(data)
}

func managedSSHRuntime() *sshRuntimeManager {
	sshRuntimeState.Lock()
	defer sshRuntimeState.Unlock()
	if sshRuntimeState.manager != nil && sshRuntimeState.manager.db != database.GetDB() {
		sshRuntimeState.manager.close()
		sshRuntimeState.manager = nil
	}
	if sshRuntimeState.manager == nil {
		ctx, cancel := context.WithCancel(context.Background())
		db := database.GetDB()
		policy := acquireManagedPolicy(db)
		m := &sshRuntimeManager{db: db, policy: policy, controller: policy.controller, entries: make(map[int]*sshRuntimeEntry), cancel: cancel, done: make(chan struct{})}
		sshRuntimeState.manager = m
		go m.watch(ctx)
	}
	return sshRuntimeState.manager
}

func stopManagedSSH() {
	sshRuntimeState.Lock()
	defer sshRuntimeState.Unlock()
	if m := sshRuntimeState.manager; m != nil {
		m.close()
		sshRuntimeState.manager = nil
	}
}

func (m *sshRuntimeManager) close() {
	m.cancel()
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.entries {
		entry.stop()
	}
	m.policy.Close()
}

func (entry *sshRuntimeEntry) stop() {
	if entry.server != nil {
		entry.server.Close()
		entry.server = nil
	}
	entry.serveDone = nil
}

func (entry *sshRuntimeEntry) report(reason string) {
	if reason == entry.lastError {
		return
	}
	entry.lastError = reason
	if reason != "" {
		logger.Warningf("managed SSH inbound %d protected: %s", entry.inbound.Id, reason)
	}
}

func sshRuntimeClients(db *gorm.DB, inboundID int) ([]sshtunnel.Client, []routedbridge.ClientBinding, error) {
	var records []model.ClientRecord
	if err := db.Model(&model.ClientRecord{}).Joins("JOIN client_inbounds ci ON ci.client_id = clients.id").
		Where("ci.inbound_id = ?", inboundID).Order("clients.id").Find(&records).Error; err != nil {
		return nil, nil, err
	}
	var clients []sshtunnel.Client
	var bindings []routedbridge.ClientBinding
	for _, record := range records {
		bindings = append(bindings, routedbridge.ClientBinding{PolicyID: record.PolicyID, Email: record.Email})
		if !record.Enable {
			continue
		}
		client, err := sshClientBinding(*record.ToClient(), record.PolicyID)
		if err != nil {
			return nil, nil, err
		}
		clients = append(clients, client)
	}
	return clients, bindings, nil
}

type sshRuntimePlan struct {
	entries   map[int]*sshRuntimeEntry
	configs   map[int]sshtunnel.Config
	outbounds []sshoutbound.Outbound
}

func buildManagedSSH(cfg *xray.Config, inbounds []*model.Inbound) (*sshRuntimePlan, error) {
	db := database.GetDB()
	plan := &sshRuntimePlan{entries: make(map[int]*sshRuntimeEntry), configs: make(map[int]sshtunnel.Config)}
	for _, inbound := range inbounds {
		if inbound.Protocol != model.SSH || !inbound.Enable || inbound.NodeID != nil {
			continue
		}
		var settings sshInboundSettings
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			return nil, errors.New("invalid stored SSH settings")
		}
		hostKey, err := ssh.ParsePrivateKey([]byte(settings.HostKey))
		if err != nil {
			return nil, errors.New("invalid stored SSH host key")
		}
		clients, bindings, err := sshRuntimeClients(db, inbound.Id)
		if err != nil {
			return nil, err
		}
		if len(clients) == 0 {
			continue
		}
		entry, err := compiledSSHEntry(db, inbound, settings, bindings)
		if err != nil {
			return nil, err
		}
		if err := entry.bridge.Apply(cfg); err != nil {
			return nil, err
		}
		plan.entries[inbound.Id] = entry
		plan.configs[inbound.Id] = sshtunnel.Config{InboundTag: inbound.Tag, HostKey: hostKey, Clients: clients, Authenticated: startManagedSSHClient}
	}
	sshRuntimeState.Lock()
	if sshRuntimeState.compiledDB == db {
		for id := range sshRuntimeState.compiled {
			if plan.entries[id] == nil {
				delete(sshRuntimeState.compiled, id)
			}
		}
	}
	sshRuntimeState.Unlock()
	return plan, nil
}

func compiledSSHEntry(db *gorm.DB, inbound *model.Inbound, settings sshInboundSettings, bindings []routedbridge.ClientBinding) (*sshRuntimeEntry, error) {
	fingerprint := sshConfigFingerprint([]any{inbound.Tag, sshListenAddress(inbound), settings.HostKey, settings.BridgePort, bindings})
	sshRuntimeState.Lock()
	defer sshRuntimeState.Unlock()
	if m := sshRuntimeState.manager; m != nil && m.db == db {
		m.mu.Lock()
		entry := m.entries[inbound.Id]
		m.mu.Unlock()
		if entry != nil && entry.fingerprint == fingerprint {
			return entry, nil
		}
	}
	if sshRuntimeState.compiledDB != db {
		sshRuntimeState.compiledDB = db
		sshRuntimeState.compiled = make(map[int]*sshRuntimeEntry)
	}
	if entry := sshRuntimeState.compiled[inbound.Id]; entry != nil && entry.fingerprint == fingerprint {
		return entry, nil
	}
	address, err := netip.ParseAddrPort(net.JoinHostPort("127.0.0.1", strconv.Itoa(settings.BridgePort)))
	if err != nil {
		return nil, errors.New("invalid stored SSH bridge port")
	}
	bridge, err := routedbridge.New(inbound.Tag, address, bindings)
	if err != nil {
		return nil, err
	}
	entry := &sshRuntimeEntry{inbound: inbound, fingerprint: fingerprint, bridge: bridge}
	sshRuntimeState.compiled[inbound.Id] = entry
	return entry, nil
}

func stageSSHRuntime(cfg *xray.Config) func() {
	sshRuntimeState.Lock()
	m := sshRuntimeState.manager
	sshRuntimeState.Unlock()
	if m == nil {
		return func() {}
	}
	fingerprint := sshConfigFingerprint(cfg)
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

func (plan *sshRuntimePlan) apply(cfg *xray.Config) {
	if len(plan.entries) == 0 {
		stopManagedSSH()
		return
	}
	m := managedSSHRuntime()
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
	m.expected = sshConfigFingerprint(cfg)
	m.pending = [32]byte{}
}

func (m *sshRuntimeManager) watch(ctx context.Context) {
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

func (m *sshRuntimeManager) reconcile(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	process := currentXrayProcess()
	matched := false
	if process != nil && process.IsRunning() {
		fingerprint := sshConfigFingerprint(process.GetConfig())
		matched = fingerprint == m.expected || fingerprint == m.pending
	}
	if !matched {
		for _, entry := range m.entries {
			entry.stop()
			entry.report("waiting for the applied Xray configuration")
		}
		return
	}
	policies, err := m.loadClientPolicies(ctx)
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
				if policy.Scope != "" && policy.Scope != "local" {
					err = ErrClientPolicyUnsupported
				} else {
					operationCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
					err = m.controller.Configure(operationCtx, client.PolicyID, policyflow.Rates{Upload: policy.UploadBps, Download: policy.DownloadBps})
					cancel()
				}
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
			case <-entry.serveDone:
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
		server, err := sshtunnel.NewServer(entry.config, m.controller, func(ctx context.Context, dest sshtunnel.Destination) (io.ReadWriteCloser, error) {
			return bridge.DialTCP(ctx, dest.PolicyID, dest.Source, dest.Host, dest.Port)
		})
		if err != nil {
			entry.report("invalid server configuration")
			continue
		}
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", sshListenAddress(entry.inbound))
		if err != nil {
			server.Close()
			entry.report("listener unavailable")
			continue
		}
		entry.server = server
		entry.report("")
		done := make(chan error, 1)
		entry.serveDone = done
		go func() { defer listener.Close(); done <- server.Serve(listener) }()
	}
}

func (m *sshRuntimeManager) loadClientPolicies(ctx context.Context) (map[string]model.ClientPolicySettings, error) {
	ids := make([]string, 0)
	for _, entry := range m.entries {
		if !entry.suspended {
			for _, client := range entry.config.Clients {
				ids = append(ids, client.PolicyID)
			}
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	policies := make(map[string]model.ClientPolicySettings, len(ids))
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for _, part := range chunkStrings(ids, sqlInChunk) {
		var rows []model.ClientPolicySettings
		if err := m.db.WithContext(ctx).Where("policy_id IN ?", part).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Scope != "local" {
				return nil, ErrClientPolicyUnsupported
			}
			if row.UploadBps < 0 || row.UploadBps > clientpolicy.MaxRate || row.DownloadBps < 0 || row.DownloadBps > clientpolicy.MaxRate {
				return nil, clientpolicy.ErrInvalidRate
			}
			policies[row.PolicyID] = row
		}
	}
	return policies, nil
}

func NotifySSHChange(inboundID int, full bool) error {
	sshRuntimeState.Lock()
	m := sshRuntimeState.manager
	sshRuntimeState.Unlock()
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
	clients, _, err := sshRuntimeClients(m.db, inboundID)
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
