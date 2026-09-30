package policy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/dokodemo"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/tcp"
	"github.com/xtls/xray-core/transport/internet/websocket"
)

func TestTunnelSourceACLTypedHandlerRejectsUntrustedPeer(t *testing.T) {
	cases := []struct {
		name   string
		change func(*proxyman.ReceiverConfig)
	}{
		{"socket-proxy", func(c *proxyman.ReceiverConfig) {
			c.StreamSettings = &internet.StreamConfig{SocketSettings: &internet.SocketConfig{AcceptProxyProtocol: true}}
		}},
		{"raw-proxy", func(c *proxyman.ReceiverConfig) {
			c.StreamSettings = &internet.StreamConfig{TransportSettings: []*internet.TransportConfig{{ProtocolName: "tcp", Settings: serial.ToTypedMessage(&tcp.Config{AcceptProxyProtocol: true})}}}
		}},
		{"websocket", func(c *proxyman.ReceiverConfig) {
			c.StreamSettings = &internet.StreamConfig{ProtocolName: "websocket", TransportSettings: []*internet.TransportConfig{{ProtocolName: "websocket", Settings: serial.ToTypedMessage(&websocket.Config{})}}}
		}},
		{"unix", func(c *proxyman.ReceiverConfig) {
			c.Listen = xnet.NewIPOrDomain(xnet.DomainAddress("/tmp/tunnel-source-acl-typed.sock"))
			c.PortList = nil
		}},
		{"localhost-unix", func(c *proxyman.ReceiverConfig) {
			c.Listen = xnet.NewIPOrDomain(xnet.DomainAddress("localhost"))
			c.PortList = nil
		}},
		{"empty-ports", func(c *proxyman.ReceiverConfig) { c.PortList = &xnet.PortList{} }},
		{"nil-port-range", func(c *proxyman.ReceiverConfig) { c.PortList.Range = append(c.PortList.Range, nil) }},
		{"reversed-port-range", func(c *proxyman.ReceiverConfig) { c.PortList.Range = []*xnet.PortRange{{From: 9001, To: 9000}} }},
		{"out-of-range-port", func(c *proxyman.ReceiverConfig) { c.PortList.Range = []*xnet.PortRange{{From: 65536, To: 65536}} }},
		{"secondary-zero-port", func(c *proxyman.ReceiverConfig) {
			c.PortList.Range = append(c.PortList.Range, &xnet.PortRange{})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg conf.Config
			if err := json.Unmarshal([]byte(`{"inbounds":[{"listen":"127.0.0.1","port":24321,"protocol":"tunnel","settings":{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":80,"allowedSourceCidrs":["127.0.0.1/32"]}}],"outbounds":[{"protocol":"freedom"}]}`), &cfg); err != nil {
				t.Fatal(err)
			}
			pb, err := cfg.Build()
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := pb.Inbound[0].ReceiverSettings.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			receiver := parsed.(*proxyman.ReceiverConfig)
			tc.change(receiver)
			pb.Inbound[0].ReceiverSettings = serial.ToTypedMessage(receiver)
			instance, err := core.New(pb)
			if instance != nil {
				_ = instance.Close()
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "source") {
				t.Fatalf("typed handler bypassed source ACL restrictions or failed for unrelated reason: %v", err)
			}
		})
	}
}

func TestTunnelSourceACLTypedHandlerRejectsInvalidCIDRs(t *testing.T) {
	for _, prefix := range []string{"invalid", "127.0.0.1", "::ffff:127.0.0.1/128"} {
		t.Run(prefix, func(t *testing.T) {
			var cfg conf.Config
			if err := json.Unmarshal([]byte(`{"inbounds":[{"listen":"127.0.0.1","port":24321,"protocol":"tunnel","settings":{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":80}}],"outbounds":[{"protocol":"freedom"}]}`), &cfg); err != nil {
				t.Fatal(err)
			}
			pb, err := cfg.Build()
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := pb.Inbound[0].ProxySettings.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			proxy := parsed.(*dokodemo.Config)
			proxy.AllowedSourceCidrs = []string{prefix}
			pb.Inbound[0].ProxySettings = serial.ToTypedMessage(proxy)
			instance, err := core.New(pb)
			if instance != nil {
				_ = instance.Close()
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "source") {
				t.Fatalf("typed proxy accepted invalid source CIDR: %v", err)
			}
		})
	}
}
