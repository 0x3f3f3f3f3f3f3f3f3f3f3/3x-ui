package policy_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/features/outbound"
)

func TestTunnelRoutingFailureDoesNotUseDefaultOutbound(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, tc := range []struct {
			name, selector, fallback, inbound, outbound string
			allow                                       bool
		}{
			{name: "no-matching-rule", selector: "absent-", inbound: "other", allow: true},
			{name: "empty-matched-balancer", selector: "absent-", inbound: "owned"},
			{name: "healthy-balancer", selector: "selected-", inbound: "owned", allow: true},
			{name: "explicit-direct-fallback", selector: "absent-", fallback: "default-direct", inbound: "owned", allow: true},
			{name: "explicit-block-fallback", selector: "absent-", fallback: "block", inbound: "owned"},
			{name: "missing-outbound", inbound: "owned", outbound: "removed"},
		} {
			t.Run(fmt.Sprintf("managed=%v/%s", managed, tc.name), func(t *testing.T) {
				target, received := loopbackEchoTarget(t, "tcp")
				listen := port(t)
				owner := ""
				if managed {
					owner = "owner"
				}
				settings := map[string]any{"allowedNetwork": "tcp", "rewriteAddress": "127.0.0.1", "rewritePort": target, "clientId": owner}
				rule := map[string]any{"type": "field", "inboundTag": []string{tc.inbound}}
				routing := map[string]any{"rules": []any{rule}}
				if tc.outbound != "" {
					rule["outboundTag"] = tc.outbound
				} else {
					rule["balancerTag"] = "chosen"
					balancer := map[string]any{"tag": "chosen", "selector": []string{tc.selector}, "strategy": map[string]any{"type": "random"}}
					if tc.fallback != "" {
						balancer["fallbackTag"] = tc.fallback
					}
					routing["balancers"] = []any{balancer}
				}
				freedom := func(tag string) map[string]any {
					return map[string]any{"tag": tag, "protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}}}
				}
				config := map[string]any{
					"log":          map[string]any{"loglevel": "error"},
					"observatory":  map[string]any{},
					"clientPolicy": map[string]any{"policies": []any{map[string]any{"clientId": "owner", "version": 1, "enabled": true, "multiplierMicros": 1500000, "burstBytes": 65536}}},
					"inbounds":     []any{map[string]any{"tag": "owned", "listen": "127.0.0.1", "port": listen, "protocol": "tunnel", "settings": settings}},
					"outbounds":    []any{freedom("default-direct"), freedom("selected-1"), map[string]any{"tag": "block", "protocol": "blackhole"}},
					"routing":      routing,
				}
				instance := start(t, loopbackJSON(t, config))
				engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
				flow := loopbackFlow(t, "tcp", listen)
				if tc.allow {
					exchange(t, flow, []byte("abcdef"))
				} else {
					_ = flow.SetDeadline(time.Now().Add(time.Second))
					_, _ = flow.Write([]byte("must-not-arrive"))
					var timeout net.Error
					if _, err := flow.Read(make([]byte, 32)); err == nil || errors.As(err, &timeout) && timeout.Timeout() {
						t.Fatalf("routing failure did not close without direct fallback: %v", err)
					}
				}
				want := int64(0)
				usage := clientpolicy.Usage{}
				if tc.allow {
					want = 6
					if managed {
						usage = clientpolicy.Usage{RawUpload: 6, RawDownload: 6, BilledBytes: 18}
					}
				}
				snapshot, err := engine.Snapshot("owner")
				if err != nil || snapshot.Usage != usage || received.Load() != want {
					t.Fatalf("wrong routing path/ledger: %+v %v target=%d want=%d", snapshot, err, received.Load(), want)
				}
			})
		}
	}
}

func TestTunnelRoutingRemovalKeepsExplicitFallbackAndSibling(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fallback=%v", network, fallback), func(t *testing.T) {
				defaultPort, defaultReceived := loopbackEchoTarget(t, network)
				selectedPort, selectedReceived := loopbackEchoTarget(t, network)
				listen, siblingListen := port(t), port(t)
				config := loopbackTunnelConfig(0, network, false, listen, defaultPort)
				inbounds := config["inbounds"].([]any)
				inbounds = append(inbounds, map[string]any{"tag": "sibling", "listen": "127.0.0.1", "port": siblingListen, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": network, "rewriteAddress": "127.0.0.1", "rewritePort": defaultPort, "clientId": "owner"}})
				config["inbounds"] = inbounds
				config["outbounds"] = []any{
					map[string]any{"tag": "default-direct", "protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}}},
					map[string]any{"tag": "selected-1", "protocol": "freedom", "settings": map[string]any{"redirect": fmt.Sprintf("127.0.0.1:%d", selectedPort), "finalRules": []any{map[string]any{"action": "allow"}}}},
				}
				balancer := map[string]any{"tag": "chosen", "selector": []string{"selected-"}, "strategy": map[string]any{"type": "random"}}
				if fallback {
					balancer["fallbackTag"] = "default-direct"
				}
				config["observatory"] = map[string]any{}
				config["routing"] = map[string]any{
					"rules": []any{
						map[string]any{"type": "field", "inboundTag": []string{"owned"}, "balancerTag": "chosen"},
						map[string]any{"type": "field", "inboundTag": []string{"sibling"}, "outboundTag": "default-direct"},
					},
					"balancers": []any{balancer},
				}
				instance := start(t, loopbackJSON(t, config))
				engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
				flow, sibling := loopbackFlow(t, network, listen), loopbackFlow(t, network, siblingListen)
				exchange(t, flow, []byte("chosen"))
				exchange(t, sibling, []byte("siblng"))
				before, err := engine.Snapshot("owner")
				if err != nil || before.Usage != (clientpolicy.Usage{RawUpload: 12, RawDownload: 12, BilledBytes: 36}) || before.ActiveSessions != 2 || defaultReceived.Load() != 6 || selectedReceived.Load() != 6 {
					t.Fatalf("selected route did not use its own target: %+v %v default=%d selected=%d", before, err, defaultReceived.Load(), selectedReceived.Load())
				}
				manager := instance.GetFeature(outbound.ManagerType()).(outbound.Manager)
				if err := manager.RemoveHandler(context.Background(), "selected-1"); err != nil {
					t.Fatal(err)
				}
				if network == "tcp" {
					_ = flow.SetReadDeadline(time.Now().Add(time.Second))
					var timeout net.Error
					if _, err := flow.Read(make([]byte, 1)); err == nil || errors.As(err, &timeout) && timeout.Timeout() {
						t.Fatalf("removed outbound retained the old stream: %v", err)
					}
				}
				newFlow := loopbackFlow(t, network, listen)
				if fallback {
					exchange(t, newFlow, []byte("fallbk"))
				} else {
					denied := []net.Conn{newFlow}
					if network == "udp" {
						denied = append(denied, flow)
					}
					for _, flow := range denied {
						_ = flow.SetDeadline(time.Now().Add(200 * time.Millisecond))
						_, _ = flow.Write([]byte("must-not-arrive"))
						var timeout net.Error
						if _, err := flow.Read(make([]byte, 32)); err == nil || network == "tcp" && errors.As(err, &timeout) && timeout.Timeout() {
							t.Fatalf("empty balancer did not reject flow: %v", err)
						}
					}
				}
				exchange(t, sibling, []byte("alive!"))
				raw, targetBytes, sessions := uint64(18), int64(12), 1
				if fallback {
					raw, targetBytes, sessions = 24, 18, 2
				}
				after, err := engine.Snapshot("owner")
				deadline := time.Now().Add(time.Second)
				for err == nil && after.ActiveSessions != sessions && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
					after, err = engine.Snapshot("owner")
				}
				if err != nil || after.Usage != (clientpolicy.Usage{RawUpload: raw, RawDownload: raw, BilledBytes: raw * 3}) || after.ActiveSessions != sessions || defaultReceived.Load() != targetBytes || selectedReceived.Load() != 6 {
					t.Fatalf("removal used an implicit fallback or changed sibling billing: %+v %v default=%d selected=%d", after, err, defaultReceived.Load(), selectedReceived.Load())
				}
			})
		}
	}
}
