package routedbridge

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Apply adds a private routed entry after normal panel policy generation, without changing user rules.
func (b *Bridge) Apply(cfg *xray.Config) error {
	if cfg == nil {
		return ErrConfig
	}
	for _, inbound := range cfg.InboundConfigs {
		if inbound.Tag == b.tag || inbound.Port == int(b.address.Port()) {
			return fmt.Errorf("%w: bridge inbound tag or port is already in use", ErrConfig)
		}
	}
	policy := make(map[string]any)
	if len(cfg.Policy) > 0 {
		if err := json.Unmarshal(cfg.Policy, &policy); err != nil || policy == nil {
			return fmt.Errorf("%w: invalid Xray policy", ErrConfig)
		}
	}
	levels := make(map[string]any)
	if existing, ok := policy["levels"]; ok {
		var valid bool
		levels, valid = existing.(map[string]any)
		if !valid {
			return fmt.Errorf("%w: invalid Xray policy levels", ErrConfig)
		}
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("%w: invalid Xray configuration", ErrConfig)
	}
	var all any
	if err := json.Unmarshal(encoded, &all); err != nil {
		return ErrConfig
	}
	used := make(map[uint32]bool)
	collectLevels(all, used)
	level := uint32(0xffffffff)
	for {
		_, defined := levels[strconv.FormatUint(uint64(level), 10)]
		if !defined && !used[level] {
			break
		}
		if level == 0 {
			return fmt.Errorf("%w: no separate bridge policy level", ErrConfig)
		}
		level--
	}
	base, _ := levels["0"].(map[string]any)
	bridgePolicy := make(map[string]any)
	maps.Copy(bridgePolicy, base)
	bridgePolicy["statsUserUplink"], bridgePolicy["statsUserDownlink"], bridgePolicy["statsUserOnline"] = false, false, false
	levels[strconv.FormatUint(uint64(level), 10)] = bridgePolicy
	policy["levels"] = levels
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return ErrConfig
	}
	ids := make([]string, 0, len(b.clients))
	for id := range b.clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	accounts := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		client := b.clients[id]
		accounts = append(accounts, map[string]string{"user": client.Email, "pass": client.password})
	}
	settings, err := json.Marshal(map[string]any{"auth": "password", "accounts": accounts, "udp": false, "userLevel": level})
	if err != nil {
		return ErrConfig
	}
	listen, err := json.Marshal(b.address.Addr().String())
	if err != nil {
		return ErrConfig
	}
	cfg.Policy = policyJSON
	cfg.InboundConfigs = append(cfg.InboundConfigs, xray.InboundConfig{
		Tag: b.tag, Listen: listen, Port: int(b.address.Port()), Protocol: "socks", Settings: settings,
		StreamSettings: json_util.RawMessage(`{"network":"raw","sockopt":{"acceptProxyProtocol":true}}`),
	})
	return nil
}

func collectLevels(value any, used map[uint32]bool) {
	switch v := value.(type) {
	case map[string]any:
		for key, field := range v {
			if key == "level" || key == "userLevel" {
				if number, ok := field.(float64); ok && number >= 0 && number <= 0xffffffff && float64(uint32(number)) == number {
					used[uint32(number)] = true
				}
			}
			collectLevels(field, used)
		}
	case []any:
		for _, field := range v {
			collectLevels(field, used)
		}
	}
}
