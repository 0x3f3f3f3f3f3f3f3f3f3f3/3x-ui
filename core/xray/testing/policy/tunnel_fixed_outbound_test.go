package policy_test

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
)

func TestTunnelFixedOutboundOverridesRouting(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		for _, tc := range []struct {
			name, fixed, routed string
			allow               bool
		}{
			{"inherit", "", "selected", true},
			{"fixed-direct", "selected", "block", true},
			{"fixed-block", "block", "default-direct", false},
			{"missing-fixed", "removed", "default-direct", false},
			{"fixed-loopback", "loop", "block", true},
			{"fixed-socks", "proxy-socks", "block", true},
			{"fixed-http", "proxy-http", "block", true},
		} {
			t.Run(network+"/"+tc.name, func(t *testing.T) {
				if network == "udp" && tc.fixed == "proxy-http" {
					tc.allow, tc.routed = false, "default-direct"
				}
				defaultPort, defaultReceived := loopbackEchoTarget(t, network)
				selectedPort, selectedReceived := loopbackEchoTarget(t, network)
				listen, proxyPort := port(t), port(t)
				config := loopbackTunnelConfig(0, network, false, listen, defaultPort)
				config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["outboundTag"] = tc.fixed
				config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "proxy-in", "listen": "127.0.0.1", "port": proxyPort, "protocol": "mixed", "settings": map[string]any{"auth": "noauth", "udp": true}})
				config["outbounds"] = []any{
					map[string]any{"tag": "default-direct", "protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}}},
					map[string]any{"tag": "selected", "protocol": "freedom", "settings": map[string]any{"redirect": fmt.Sprintf("127.0.0.1:%d", selectedPort), "finalRules": []any{map[string]any{"action": "allow"}}}},
					map[string]any{"tag": "block", "protocol": "blackhole"},
					map[string]any{"tag": "proxy-socks", "protocol": "socks", "settings": map[string]any{"address": "127.0.0.1", "port": proxyPort}},
					map[string]any{"tag": "proxy-http", "protocol": "http", "settings": map[string]any{"address": "127.0.0.1", "port": proxyPort}},
					map[string]any{"tag": "loop", "protocol": "loopback", "settings": map[string]any{"inboundTag": "virtual"}},
				}
				config["routing"] = map[string]any{"rules": []any{
					map[string]any{"type": "field", "inboundTag": []string{"owned"}, "outboundTag": tc.routed},
					map[string]any{"type": "field", "inboundTag": []string{"virtual", "proxy-in"}, "outboundTag": "selected"},
				}}
				instance := start(t, loopbackJSON(t, config))
				engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
				flow := loopbackFlow(t, network, listen)
				usage, want := clientpolicy.Usage{}, int64(0)
				if tc.allow {
					exchange(t, flow, []byte("chosen"))
					usage, want = clientpolicy.Usage{RawUpload: 6, RawDownload: 6, BilledBytes: 18}, 6
				} else {
					_ = flow.SetDeadline(time.Now().Add(200 * time.Millisecond))
					_, _ = flow.Write([]byte("must-not-arrive"))
					var timeout net.Error
					if _, err := flow.Read(make([]byte, 32)); err == nil || network == "tcp" && errors.As(err, &timeout) && timeout.Timeout() {
						t.Fatalf("fixed route failed to reject traffic: %v", err)
					}
					if network == "udp" && tc.fixed == "block" {
						usage = clientpolicy.Usage{RawUpload: 15, BilledBytes: 22, Remainder: 500000}
					}
				}
				snapshot, err := engine.Snapshot("owner")
				if err != nil || snapshot.Usage != usage || selectedReceived.Load() != want || defaultReceived.Load() != 0 {
					t.Fatalf("fixed selection or once-only metering failed: %+v %v selected=%d want=%d default=%d", snapshot, err, selectedReceived.Load(), want, defaultReceived.Load())
				}
			})
		}
	}
}
