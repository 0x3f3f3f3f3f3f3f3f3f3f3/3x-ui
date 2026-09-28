package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/routedbridge"
	"github.com/mhsanaei/3x-ui/v3/internal/sshtunnel"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var sshRuntimeState struct {
	sync.Mutex
	manager *sshRuntimeManager
}

type sshRuntimeManager struct {
	mu         sync.Mutex
	db         *gorm.DB
	controller *policyflow.Controller
	entries    map[int]*sshRuntimeEntry
	expected   [32]byte
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
		m := &sshRuntimeManager{db: db, controller: policyflow.NewController(database.NewClientUsageLedger(db), "local/managed-services"), entries: make(map[int]*sshRuntimeEntry), cancel: cancel, done: make(chan struct{})}
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
	m.controller.Close()
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

func prepareManagedSSH(cfg *xray.Config, inbounds []*model.Inbound) error {
	var wanted []*model.Inbound
	for _, inbound := range inbounds {
		if inbound.Protocol == model.SSH && inbound.Enable && inbound.NodeID == nil {
			wanted = append(wanted, inbound)
		}
	}
	sshRuntimeState.Lock()
	exists := sshRuntimeState.manager != nil
	sshRuntimeState.Unlock()
	if len(wanted) == 0 && !exists {
		return nil
	}
	m := managedSSHRuntime()
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[int]*sshRuntimeEntry)
	configs := make(map[int]sshtunnel.Config)
	for _, inbound := range wanted {
		var settings sshInboundSettings
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			return errors.New("invalid stored SSH settings")
		}
		hostKey, err := ssh.ParsePrivateKey([]byte(settings.HostKey))
		if err != nil {
			return errors.New("invalid stored SSH host key")
		}
		clients, bindings, err := sshRuntimeClients(m.db, inbound.Id)
		if err != nil {
			return err
		}
		if len(clients) == 0 {
			continue
		}
		fingerprint := sshConfigFingerprint([]any{inbound.Tag, sshListenAddress(inbound), settings.HostKey, settings.BridgePort, bindings})
		entry := m.entries[inbound.Id]
		if entry == nil || entry.fingerprint != fingerprint {
			address, err := netip.ParseAddrPort(net.JoinHostPort("127.0.0.1", strconv.Itoa(settings.BridgePort)))
			if err != nil {
				return errors.New("invalid stored SSH bridge port")
			}
			bridge, err := routedbridge.New(inbound.Tag, address, bindings)
			if err != nil {
				return err
			}
			entry = &sshRuntimeEntry{inbound: inbound, fingerprint: fingerprint, bridge: bridge}
		}
		configs[inbound.Id] = sshtunnel.Config{InboundTag: inbound.Tag, HostKey: hostKey, Clients: clients, Authenticated: startManagedSSHClient}
		if err := entry.bridge.Apply(cfg); err != nil {
			return err
		}
		next[inbound.Id] = entry
	}
	for id, entry := range m.entries {
		if next[id] != entry {
			entry.stop()
		}
	}
	m.entries = next
	for id, entry := range next {
		entry.config = configs[id]
		entry.suspended = false
	}
	m.expected = sshConfigFingerprint(cfg)
	return nil
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
	if process == nil || !process.IsRunning() || sshConfigFingerprint(process.GetConfig()) != m.expected {
		for _, entry := range m.entries {
			entry.stop()
			entry.report("waiting for the applied Xray configuration")
		}
		return
	}
	for _, entry := range m.entries {
		if entry.suspended {
			continue
		}
		ready := true
		for _, client := range entry.config.Clients {
			operationCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			err := m.controller.Configure(operationCtx, client.PolicyID, policyflow.Rates{})
			cancel()
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
