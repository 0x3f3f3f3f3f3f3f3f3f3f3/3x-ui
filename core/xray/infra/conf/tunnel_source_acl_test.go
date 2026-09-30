package conf_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
)

func TestTunnelSourceACLConfigRejectsUnsafeInputs(t *testing.T) {
	cases := []struct {
		name    string
		sources []string
		stream  map[string]any
		listen  string
	}{
		{name: "invalid-prefix", sources: []string{"127.0.0.1/33"}},
		{name: "bare-address", sources: []string{"127.0.0.1"}},
		{name: "empty-entry", sources: []string{""}},
		{name: "mapped-prefix", sources: []string{"::ffff:127.0.0.0/120"}},
		{name: "socket-proxy", stream: map[string]any{"sockopt": map[string]any{"acceptProxyProtocol": true}}},
		{name: "raw-proxy", stream: map[string]any{"network": "raw", "rawSettings": map[string]any{"acceptProxyProtocol": true}}},
		{name: "legacy-tcp-proxy", stream: map[string]any{"network": "tcp", "tcpSettings": map[string]any{"acceptProxyProtocol": true}}},
		{name: "method-overrides-network", stream: map[string]any{"network": "raw", "method": "ws"}},
		{name: "websocket", stream: map[string]any{"network": "ws"}},
		{name: "httpupgrade", stream: map[string]any{"network": "httpupgrade"}},
		{name: "xhttp", stream: map[string]any{"network": "xhttp"}},
		{name: "http-header", stream: map[string]any{"rawSettings": map[string]any{"header": map[string]any{"type": "http"}}}},
		{name: "tcp-mask", stream: map[string]any{"finalmask": map[string]any{"tcp": []any{map[string]any{"type": "header-custom", "settings": map[string]any{}}}}}},
		{name: "udp-mask", stream: map[string]any{"finalmask": map[string]any{"udp": []any{map[string]any{"type": "header-custom", "settings": map[string]any{}}}}}},
		{name: "unix", listen: "/tmp/tunnel-source-acl.sock"},
	}
	many := make([]string, 257)
	for i := range many {
		many[i] = "127.0.0.1/32"
	}
	cases = append(cases, struct {
		name    string
		sources []string
		stream  map[string]any
		listen  string
	}{name: "too-many-prefixes", sources: many})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.sources == nil {
				tc.sources = []string{"127.0.0.1/32"}
			}
			if tc.listen == "" {
				tc.listen = "127.0.0.1"
			}
			raw := map[string]any{"listen": tc.listen, "port": 24321, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": "tcp,udp", "rewriteAddress": "127.0.0.1", "rewritePort": 80, "allowedSourceCidrs": tc.sources}}
			if tc.stream != nil {
				raw["streamSettings"] = tc.stream
			}
			data, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var cfg conf.InboundDetourConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.Build(); err == nil || !strings.Contains(strings.ToLower(err.Error()), "source") {
				t.Fatalf("unsafe source ACL accepted or failed for unrelated reason: %v", err)
			}
		})
	}
}

func TestTunnelSourceACLConfigRetainsLegacyUnrestrictedModes(t *testing.T) {
	for _, network := range []string{"raw", "tcp", "ws", "httpupgrade", "xhttp"} {
		t.Run(network, func(t *testing.T) {
			data, _ := json.Marshal(map[string]any{"listen": "127.0.0.1", "port": 24321, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": "tcp", "rewriteAddress": "127.0.0.1", "rewritePort": 80}, "streamSettings": map[string]any{"network": network}})
			var cfg conf.InboundDetourConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.Build(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTunnelSourceACLConfigAcceptsTrustedModes(t *testing.T) {
	for _, stream := range []string{`{}`, `{"network":"raw","rawSettings":{"header":{"type":"none"}}}`, `{"network":"raw","security":"tls","tlsSettings":{}}`, `{"network":"ws","method":"raw"}`, `{"network":"raw","rawSettings":{"acceptProxyProtocol":false},"tcpSettings":{"acceptProxyProtocol":true}}`} {
		for _, listen := range []string{"127.0.0.1", "::1", "localhost"} {
			t.Run(listen+stream, func(t *testing.T) {
				data := `{"listen":"` + listen + `","port":24321,"protocol":"tunnel","settings":{"allowedNetwork":"tcp,udp","rewriteAddress":"127.0.0.1","rewritePort":80,"allowedSourceCidrs":["127.0.0.1/32","::1/128"]},"streamSettings":` + stream + `}`
				var cfg conf.InboundDetourConfig
				if err := json.Unmarshal([]byte(data), &cfg); err != nil {
					t.Fatal(err)
				}
				if _, err := cfg.Build(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestTunnelSourceACLConfigRejectsMalformedField(t *testing.T) {
	for _, value := range []string{`"127.0.0.1/32"`, `[3]`, `{}`, `true`} {
		t.Run(value, func(t *testing.T) {
			var cfg conf.InboundDetourConfig
			data := `{"listen":"127.0.0.1","port":24321,"protocol":"tunnel","settings":{"allowedNetwork":"tcp","allowedSourceCidrs":` + value + `}}`
			if err := json.Unmarshal([]byte(data), &cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.Build(); err == nil {
				t.Fatal("malformed ACL silently accepted")
			}
		})
	}
}
