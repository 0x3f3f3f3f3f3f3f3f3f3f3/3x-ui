package policy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/features/stats"
)

func loopbackTunnelConfig(hops int, network string, sniff bool, listen, target int) map[string]any {
	outbounds := []any{map[string]any{"tag": "block", "protocol": "blackhole"}}
	rules := []any{}
	incoming := "owned"
	for hop := 1; hop <= hops; hop++ {
		outbound, next := fmt.Sprintf("loop-%d", hop), fmt.Sprintf("hop-%d", hop)
		settings := map[string]any{"inboundTag": next}
		if sniff {
			settings["sniffing"] = map[string]any{"enabled": true, "destOverride": []string{"http"}, "routeOnly": true}
		}
		outbounds = append(outbounds, map[string]any{"tag": outbound, "protocol": "loopback", "settings": settings})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{incoming}, "outboundTag": outbound})
		incoming = next
	}
	outbounds = append(outbounds, map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}}})
	rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{incoming}, "outboundTag": "direct"})
	return map[string]any{
		"log":          map[string]any{"loglevel": "error"},
		"stats":        map[string]any{},
		"policy":       map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}}},
		"clientPolicy": map[string]any{"policies": []any{map[string]any{"clientId": "owner", "version": 1, "enabled": true, "multiplierMicros": 1500000, "burstBytes": 65536}}},
		"inbounds":     []any{map[string]any{"tag": "owned", "listen": "127.0.0.1", "port": listen, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": network, "rewriteAddress": "127.0.0.1", "rewritePort": target, "clientId": "owner", "email": "loopback-owner"}}},
		"outbounds":    outbounds, "routing": map[string]any{"rules": rules},
	}
}

func loopbackEchoTarget(t *testing.T, network string) (int, *atomic.Int64) {
	t.Helper()
	received := new(atomic.Int64)
	if network == "udp" {
		target, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = target.Close() })
		go func() {
			buffer := make([]byte, 1024)
			for {
				n, addr, err := target.ReadFrom(buffer)
				if err != nil {
					return
				}
				received.Add(int64(n))
				_, _ = target.WriteTo(buffer[:n], addr)
			}
		}()
		return target.LocalAddr().(*net.UDPAddr).Port, received
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buffer := make([]byte, 1024)
				for {
					n, err := conn.Read(buffer)
					if n > 0 {
						received.Add(int64(n))
						if _, writeErr := conn.Write(buffer[:n]); writeErr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return target.Addr().(*net.TCPAddr).Port, received
}

func loopbackJSON(t *testing.T, config map[string]any) string {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func loopbackFlow(t *testing.T, network string, listen int) net.Conn {
	t.Helper()
	flow, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", listen), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flow.Close() })
	return flow
}

func TestTunnelLoopbackRoutingMetersPayloadOnce(t *testing.T) {
	for _, hops := range []int{1, 2} {
		for _, mode := range []string{"tcp", "tcp-sniff", "udp", "udp-domain-sniff"} {
			t.Run(fmt.Sprintf("hops=%d/%s", hops, mode), func(t *testing.T) {
				network, sniff := "tcp", strings.Contains(mode, "sniff")
				if strings.HasPrefix(mode, "udp") {
					network = "udp"
				}
				payload := []byte("abcdef")
				if sniff && network == "tcp" {
					payload = []byte("GET / HTTP/1.1\r\nHost: example.test\r\n\r\n")
				}
				target, received := loopbackEchoTarget(t, network)
				listen := port(t)
				config := loopbackTunnelConfig(hops, network, sniff, listen, target)
				if mode == "udp-domain-sniff" {
					config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["rewriteAddress"] = "localhost"
					config["outbounds"].([]any)[1].(map[string]any)["targetStrategy"] = "ForceIPv4"
				}
				instance := start(t, loopbackJSON(t, config))
				engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
				counters := instance.GetFeature(stats.ManagerType()).(stats.Manager)
				var flows []net.Conn
				for count := 1; count <= 2; count++ {
					flow := loopbackFlow(t, network, listen)
					flows = append(flows, flow)
					exchange(t, flow, payload)
					raw := uint64(len(payload) * count)
					snap, err := engine.Snapshot("owner")
					if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: raw, RawDownload: raw, BilledBytes: raw * 3}) || snap.ActiveSessions != count {
						t.Fatalf("same payload charged/registered more than once: %+v %v", snap, err)
					}
					for _, direction := range []string{"uplink", "downlink"} {
						counter := counters.GetCounter("user>>>loopback-owner>>>traffic>>>" + direction)
						if counter == nil || counter.Value() != int64(raw) {
							t.Fatalf("loopback duplicated legacy %s counter: %v", direction, counter)
						}
					}
				}
				if err := engine.Apply(clientpolicy.Policy{ClientID: "owner", Version: 2, Enabled: false, Multiplier: 1500000, BurstBytes: 65536}); err != nil {
					t.Fatal(err)
				}
				for _, flow := range flows {
					_ = flow.SetDeadline(time.Now().Add(200 * time.Millisecond))
					if network == "udp" {
						_, _ = flow.Write([]byte("disabled"))
					}
					var timeout net.Error
					if _, err := flow.Read(make([]byte, 1)); err == nil || network == "tcp" && errors.As(err, &timeout) && timeout.Timeout() {
						t.Fatalf("loopback retained disabled flow: %v", err)
					}
				}
				snap, err := engine.Snapshot("owner")
				raw := uint64(len(payload) * 2)
				if err != nil || snap.ActiveSessions != 0 || snap.Usage != (clientpolicy.Usage{RawUpload: raw, RawDownload: raw, BilledBytes: raw * 3}) || received.Load() != int64(raw) {
					t.Fatalf("disabled flow leaked or consumed payload: %+v %v target=%d", snap, err, received.Load())
				}
			})
		}
	}
}

func TestTunnelLoopbackQuotaUsesPayloadOnce(t *testing.T) {
	target, received := loopbackEchoTarget(t, "tcp")
	listen := port(t)
	config := loopbackTunnelConfig(2, "tcp", false, listen, target)
	config["clientPolicy"].(map[string]any)["policies"].([]any)[0].(map[string]any)["quotaBytes"] = 60
	instance := start(t, loopbackJSON(t, config))
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	first, second := loopbackFlow(t, "tcp", listen), loopbackFlow(t, "tcp", listen)
	for _, flow := range []net.Conn{first, second, first} {
		exchange(t, flow, []byte("abcdef"))
	}
	snap, err := engine.Snapshot("owner")
	want := clientpolicy.Usage{RawUpload: 18, RawDownload: 18, BilledBytes: 54}
	if err != nil || snap.Usage != want || snap.ActiveSessions != 2 {
		t.Fatalf("quota charged internal hops: %+v %v", snap, err)
	}
	_, _ = first.Write([]byte("xx"))
	for _, flow := range []net.Conn{first, second} {
		_ = flow.SetReadDeadline(time.Now().Add(time.Second))
		payload, err := io.ReadAll(flow)
		if err != nil || len(payload) > 2 {
			t.Fatalf("exhausted flow stayed open or exceeded its final payload: %q %v", payload, err)
		}
	}
	reconnect := loopbackFlow(t, "tcp", listen)
	_ = reconnect.SetDeadline(time.Now().Add(time.Second))
	_, _ = reconnect.Write([]byte("again"))
	if _, err := reconnect.Read(make([]byte, 1)); err == nil {
		t.Fatal("quota exhausted client reconnected")
	}
	snap, err = engine.Snapshot("owner")
	want = clientpolicy.Usage{RawUpload: 20, RawDownload: 20, BilledBytes: 60}
	if err != nil || snap.Usage != want || snap.Reasons != clientpolicy.ReasonQuota|clientpolicy.ReasonAuthority || snap.ActiveSessions != 0 || received.Load() != 20 {
		t.Fatalf("quota leaked payload: %+v %v target=%d", snap, err, received.Load())
	}
}

func TestTunnelLoopbackCycleDoesNotFallBackDirect(t *testing.T) {
	for _, hops := range []int{1, 2} {
		t.Run(fmt.Sprint(hops), func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			listen := port(t)
			config := loopbackTunnelConfig(hops, "tcp", false, listen, target)
			routes := config["routing"].(map[string]any)["rules"].([]any)
			routes[len(routes)-1].(map[string]any)["outboundTag"] = "loop-1"
			config["outbounds"].([]any)[0] = map[string]any{"tag": "default-direct", "protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}}}
			instance := start(t, loopbackJSON(t, config))
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			flow := loopbackFlow(t, "tcp", listen)
			_ = flow.SetDeadline(time.Now().Add(time.Second))
			_, _ = flow.Write([]byte("must-not-arrive"))
			var timeout net.Error
			if _, err := flow.Read(make([]byte, 32)); err == nil || errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatalf("cyclic routing did not terminate: %v", err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				snap, err := engine.Snapshot("owner")
				if err != nil {
					t.Fatal(err)
				}
				if snap.ActiveSessions == 0 {
					if snap.Usage != (clientpolicy.Usage{}) || received.Load() != 0 {
						t.Fatalf("cycle billed or reached direct target: %+v target=%d", snap, received.Load())
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("cycle leaked sessions: %+v", snap)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
