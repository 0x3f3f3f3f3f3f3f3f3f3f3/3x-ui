package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/sshoutbound"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var sshOutboundState struct {
	sync.Mutex
	manager *sshoutbound.Manager
	port    int
	cancel  context.CancelFunc
	done    chan struct{}
}

func sshOutboundBridgePort() (int, error) {
	value := strings.TrimSpace(os.Getenv("XUI_SSH_UPSTREAM_BRIDGE_PORT"))
	if value == "" {
		return 64901, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("XUI_SSH_UPSTREAM_BRIDGE_PORT must be between 1 and 65535")
	}
	return port, nil
}

func managedSSHOutbounds() (*sshoutbound.Manager, error) {
	port, err := sshOutboundBridgePort()
	if err != nil {
		return nil, err
	}
	sshOutboundState.Lock()
	defer sshOutboundState.Unlock()
	if sshOutboundState.manager != nil {
		if sshOutboundState.port != port {
			return nil, errors.New("SSH upstream bridge port changed; restart the panel to apply it")
		}
		return sshOutboundState.manager, nil
	}
	manager, err := sshoutbound.NewManager(port)
	if err != nil {
		return nil, err
	}
	sshOutboundState.manager = manager
	sshOutboundState.port = port
	return manager, nil
}

func renderSSHOutbounds(cfg *xray.Config) ([]sshoutbound.Outbound, error) {
	if len(cfg.OutboundConfigs) == 0 {
		return nil, nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(cfg.OutboundConfigs, &raws); err != nil {
		return nil, errors.New("invalid outbound configuration")
	}
	desired, err := sshoutbound.ParseOutbounds(raws)
	if err != nil || len(desired) == 0 {
		return desired, err
	}
	manager, err := managedSSHOutbounds()
	if err != nil {
		return nil, err
	}
	replacements := make(map[string]json.RawMessage, len(desired))
	for _, outbound := range desired {
		replacement, err := manager.Render(outbound)
		if err != nil {
			return nil, err
		}
		replacements[outbound.Tag] = replacement
	}
	for i, raw := range raws {
		var tag struct {
			Tag string `json:"tag"`
		}
		_ = json.Unmarshal(raw, &tag)
		if replacement := replacements[tag.Tag]; replacement != nil {
			raws[i] = replacement
		}
	}
	encoded, err := json.Marshal(raws)
	if err != nil {
		return nil, err
	}
	cfg.OutboundConfigs = json_util.RawMessage(encoded)
	return desired, nil
}

func prepareManagedSSHOutbounds(desired []sshoutbound.Outbound) (*sshoutbound.Prepared, error) {
	sshOutboundState.Lock()
	manager := sshOutboundState.manager
	sshOutboundState.Unlock()
	if len(desired) == 0 && (manager == nil || !manager.HasAppliedOutbounds()) {
		return nil, nil
	}
	manager, err := managedSSHOutbounds()
	if err != nil {
		return nil, err
	}
	prepared, err := manager.Prepare(desired)
	if err != nil {
		return nil, err
	}
	sshOutboundState.Lock()
	if sshOutboundState.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		sshOutboundState.cancel, sshOutboundState.done = cancel, done
		go watchSSHOutbounds(ctx, manager, done)
	}
	sshOutboundState.Unlock()
	return prepared, nil
}

func watchSSHOutbounds(ctx context.Context, manager *sshoutbound.Manager, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !lock.TryLock() {
				continue
			}
			if process := currentXrayProcess(); process == nil || !process.IsRunning() {
				_ = manager.Close()
			}
			lock.Unlock()
		}
	}
}

func stopManagedSSHOutbounds() {
	sshOutboundState.Lock()
	defer sshOutboundState.Unlock()
	if sshOutboundState.cancel != nil {
		sshOutboundState.cancel()
		<-sshOutboundState.done
		sshOutboundState.cancel, sshOutboundState.done = nil, nil
	}
	if manager := sshOutboundState.manager; manager != nil {
		_ = manager.Close()
		sshOutboundState.manager = nil
	}
}
