package outbound

import (
	"encoding/json"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/sshoutbound"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Each probe owns its bridge, so probing an edited pin cannot revoke the
// applied runtime's connections or reuse its superseded credentials.
func prepareSSHProbe(cfg *xray.Config) (func(), error) {
	var raws []json.RawMessage
	if err := json.Unmarshal(cfg.OutboundConfigs, &raws); err != nil {
		return nil, fmt.Errorf("invalid probe outbounds")
	}
	desired, err := sshoutbound.ParseOutbounds(raws)
	if err != nil {
		return nil, err
	}
	if len(desired) == 0 {
		return func() {}, nil
	}
	ports, release, err := reserveLoopbackPorts(1)
	if err != nil {
		return nil, fmt.Errorf("reserve SSH probe bridge: %w", err)
	}
	defer release()
	manager, err := sshoutbound.NewManager(ports[0])
	if err != nil {
		return nil, err
	}
	closeBridge := func() { _ = manager.Close() }
	success := false
	defer func() {
		if !success {
			closeBridge()
		}
	}()
	release()
	prepared, err := manager.Prepare(desired)
	if err != nil {
		return nil, err
	}
	prepared.Commit()
	rendered := make(map[string]json.RawMessage, len(desired))
	for _, ob := range desired {
		raw, err := manager.Render(ob)
		if err != nil {
			return nil, err
		}
		rendered[ob.Tag] = raw
	}
	for i, raw := range raws {
		var meta struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("invalid probe outbound")
		}
		if replacement := rendered[meta.Tag]; replacement != nil {
			raws[i] = replacement
		}
	}
	compiled, err := json.Marshal(raws)
	if err != nil {
		return nil, fmt.Errorf("cannot compile SSH probe outbounds")
	}
	cfg.OutboundConfigs = compiled
	success = true
	return closeBridge, nil
}
