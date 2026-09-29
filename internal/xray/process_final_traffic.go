package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xtls/xray-core/app/commander"
)

var ErrFinalTrafficPending = errors.New("final traffic settlement owns this child's counters")

func trafficControlEndpoint(config *Config) (string, error) {
	if len(config.TrafficControl) == 0 || string(config.TrafficControl) == "null" {
		return "", nil
	}
	var control struct {
		Listen string `json:"listen"`
	}
	if err := json.Unmarshal(config.TrafficControl, &control); err != nil {
		return "", err
	}
	if err := commander.ValidatePrivateUnixSocket(control.Listen); err != nil {
		return "", err
	}
	return control.Listen, nil
}

func (p *process) pinTrafficControl(ctx context.Context, endpoint string) error {
	p.trafficMu.Lock()
	defer p.trafficMu.Unlock()
	for {
		if !p.IsRunning() {
			return errors.New("core exited before traffic control negotiation")
		}
		attempt, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		client, err := dialTrafficControl(attempt, endpoint, "", p.trafficChildPID())
		cancel()
		if err == nil {
			client.connection.Close()
			p.trafficEndpoint, p.trafficBootID = endpoint, client.boot
			return nil
		}
		if errors.Is(err, ErrTrafficDrainCapability) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("traffic control negotiation: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (p *Process) TrafficDrainBootID() string {
	p.trafficMu.Lock()
	defer p.trafficMu.Unlock()
	return p.trafficBootID
}

// SettleFinalTraffic replays an uncertain batch before settling the boot-bound final delta.
// The caller holds lifecycle ownership; the callback must not call lifecycle methods.
func (p *Process) SettleFinalTraffic(ctx context.Context, settle func(*TrafficBatch) error) error {
	if settle == nil {
		return errors.New("final traffic settlement callback is required")
	}
	p.trafficMu.Lock()
	defer p.trafficMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.trafficFinalSettled {
		return nil
	}
	if p.trafficFinal == nil {
		if !p.IsControlReady() || p.trafficBootID == "" || p.trafficEndpoint == "" {
			return ErrTrafficDrainCapability
		}
		client, err := dialTrafficControl(ctx, p.trafficEndpoint, p.trafficBootID, p.trafficChildPID())
		if err != nil {
			return err
		}
		defer client.connection.Close()
		if !client.supported {
			return ErrTrafficDrainCapability
		}
		if p.trafficPending != nil {
			if err := p.commitTrafficPending(settle); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.ensureTrafficSequence(); err != nil {
			return err
		}
		p.trafficDraining = true
		values, err := client.finalCounters(ctx)
		if err != nil {
			return err
		}
		p.trafficFinal = values
	}
	if p.trafficPending == nil {
		if err := p.ensureTrafficSequence(); err != nil {
			return err
		}
		pending, err := p.trafficBatchFromCounters(p.trafficFinal, true)
		if err != nil {
			return err
		}
		p.trafficPending = pending
	}
	if err := p.commitTrafficPending(settle); err != nil {
		return err
	}
	p.trafficFinalSettled = true
	return nil
}

func (p *process) trafficChildPID() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
