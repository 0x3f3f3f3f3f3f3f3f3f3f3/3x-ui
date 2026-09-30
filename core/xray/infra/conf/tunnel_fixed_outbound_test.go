package conf_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
)

func TestTunnelFixedOutboundRejectsMalformedSelection(t *testing.T) {
	for _, protocol := range []string{"tunnel", "dokodemo-door"} {
		for _, value := range []string{`123`, `true`, `["selected"]`, `{"tag":"selected"}`} {
			t.Run(protocol+"/"+value, func(t *testing.T) {
				raw := `{"listen":"127.0.0.1","port":24321,"protocol":"` + protocol + `","settings":{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":80,"outboundTag":` + value + `}}`
				var cfg conf.InboundDetourConfig
				if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
					t.Fatal(err)
				}
				if _, err := cfg.Build(); err == nil || !strings.Contains(err.Error(), "outboundTag") {
					t.Fatalf("malformed selection silently ignored or failed for unrelated reason: %v", err)
				}
			})
		}
	}
}
