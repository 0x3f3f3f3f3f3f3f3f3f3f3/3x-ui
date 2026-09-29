package routedbridge

import (
	"bytes"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestManagedBridgeDoesNotInventMissingUDPPeer(t *testing.T) {
	binaryPath := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for missing peer protection")
	}
	binding := ClientBinding{PolicyID: uuid.NewString(), Email: "missing-peer"}
	bridge, err := NewManaged("managed", netip.MustParseAddrPort(reserveDatagramCoreAddress(t)), []ClientBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	startManagedCore(t, binaryPath, bridge, func(cfg map[string]any) {
		cfg["outbounds"] = []any{map[string]any{"protocol": "blackhole", "settings": map[string]any{"response": map[string]any{"type": "http"}}}}
	})
	conn, err := bridge.DialUDP(t.Context(), binding.PolicyID, netip.MustParseAddrPort("127.0.0.2:34567"), "127.0.0.1", 12345)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("probe")); err != nil {
		t.Fatal(err)
	}
	p := make([]byte, 4096)
	if n, peer, err := conn.ReadFrom(p); err == nil || n != 0 || peer != nil {
		t.Fatalf("unaddressed outbound payload was relabeled as a UDP peer: n=%d peer=%v err=%v", n, peer, err)
	}
}

func TestManagedBridgeRoutesOriginalUDPContext(t *testing.T) {
	binaryPath := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for actual route decisions")
	}
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	port := uint16(target.LocalAddr().(*net.UDPAddr).Port)
	bindings := []ClientBinding{{PolicyID: uuid.NewString(), Email: "alice"}, {PolicyID: uuid.NewString(), Email: "bob"}}
	for _, tc := range []struct {
		name, host, source, want string
		user                     int
		rules                    []any
		balancers                []any
	}{
		{name: "alice", host: "localhost", want: "127.0.0.2", rules: []any{map[string]any{"user": []string{"alice"}, "outboundTag": "a"}}},
		{name: "bob-same-source", host: "localhost", user: 1, want: "127.0.0.3", rules: []any{map[string]any{"user": []string{"alice"}, "outboundTag": "a"}, map[string]any{"user": []string{"bob"}, "outboundTag": "b"}}},
		{name: "domain", host: "localhost", want: "127.0.0.2", rules: []any{map[string]any{"domain": []string{"full:localhost"}, "outboundTag": "a"}}},
		{name: "ip", host: "127.0.0.1", want: "127.0.0.3", rules: []any{map[string]any{"ip": []string{"127.0.0.1"}, "outboundTag": "b"}}},
		{name: "source", host: "localhost", want: "127.0.0.2", rules: []any{map[string]any{"source": []string{"127.0.0.9"}, "sourcePort": "34567", "outboundTag": "a"}}},
		{name: "other-source-blocked", host: "localhost", source: "127.0.0.8:34567", rules: []any{map[string]any{"source": []string{"127.0.0.9"}, "outboundTag": "a"}}},
		{name: "inbound-network-port", host: "localhost", want: "127.0.0.3", rules: []any{map[string]any{"inboundTag": []string{"managed-route"}, "network": "udp", "port": strconv.Itoa(int(port)), "outboundTag": "b"}}},
		{name: "wrong-network-blocked", host: "localhost", rules: []any{map[string]any{"network": "tcp", "outboundTag": "a"}}},
		{name: "priority-block", host: "localhost", rules: []any{map[string]any{"user": []string{"alice"}, "outboundTag": "deny"}, map[string]any{"domain": []string{"full:localhost"}, "outboundTag": "a"}}},
		{name: "balancer", host: "localhost", want: "127.0.0.2", rules: []any{map[string]any{"network": "udp", "balancerTag": "chosen"}}, balancers: []any{map[string]any{"tag": "chosen", "selector": []string{"a", "b"}, "strategy": map[string]any{"type": "roundrobin"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge, err := NewManaged("managed-route", netip.MustParseAddrPort(reserveDatagramCoreAddress(t)), bindings)
			if err != nil {
				t.Fatal(err)
			}
			startManagedCore(t, binaryPath, bridge, func(cfg map[string]any) {
				direct := cfg["outbounds"].([]any)[0].(map[string]any)
				cfg["outbounds"] = []any{
					map[string]any{"tag": "deny", "protocol": "blackhole"},
					map[string]any{"tag": "a", "protocol": "freedom", "settings": direct["settings"], "streamSettings": direct["streamSettings"], "sendThrough": "127.0.0.2"},
					map[string]any{"tag": "b", "protocol": "freedom", "settings": direct["settings"], "streamSettings": direct["streamSettings"], "sendThrough": "127.0.0.3"},
				}
				cfg["routing"] = map[string]any{"domainStrategy": "AsIs", "rules": tc.rules, "balancers": tc.balancers}
			})
			source := netip.MustParseAddrPort("127.0.0.9:34567")
			if tc.source != "" {
				source = netip.MustParseAddrPort(tc.source)
			}
			probeManagedUDPRoute(t, bridge, bindings[tc.user], source, tc.host, port, target, tc.name, tc.want)
			if tc.name == "balancer" {
				probeManagedUDPRoute(t, bridge, bindings[tc.user], source, tc.host, port, target, "second", "127.0.0.3")
				probeManagedUDPRoute(t, bridge, bindings[tc.user], source, tc.host, port, target, "third", "127.0.0.2")
			}
		})
	}
}

func probeManagedUDPRoute(t *testing.T, bridge *ManagedBridge, binding ClientBinding, source netip.AddrPort, host string, port uint16, target net.PacketConn, payload, want string) {
	t.Helper()
	conn, err := bridge.DialUDP(t.Context(), binding.PolicyID, source, host, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	_ = target.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buffer := make([]byte, 100)
	n, peer, err := target.ReadFrom(buffer)
	if want == "" {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("blocked route reached target: n=%d peer=%v err=%v", n, peer, err)
		}
		return
	}
	if err != nil || !bytes.Equal(buffer[:n], []byte(payload)) || peer.(*net.UDPAddr).IP.String() != want {
		t.Fatalf("wrong exit or payload: peer=%v payload=%q err=%v; want exit %s", peer, buffer[:n], err, want)
	}
	if _, err := target.WriteTo(buffer[:n], peer); err != nil {
		t.Fatal(err)
	}
	n, peer, err = conn.ReadFrom(buffer)
	if err != nil || !bytes.Equal(buffer[:n], []byte(payload)) || peer.String() != target.LocalAddr().String() {
		t.Fatalf("wrong routed reply: peer=%v payload=%q err=%v", peer, buffer[:n], err)
	}
}
