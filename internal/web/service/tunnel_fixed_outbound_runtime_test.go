//go:build linux

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

func fixedOutboundEchoTarget(t *testing.T, network string) (int, *atomic.Int64) {
	t.Helper()
	var received atomic.Int64
	if network == "udp" {
		listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		go func() {
			data := make([]byte, 4096)
			for {
				n, peer, err := listener.ReadFrom(data)
				if err != nil {
					return
				}
				received.Add(int64(n))
				_, _ = listener.WriteTo(data[:n], peer)
			}
		}()
		return listener.LocalAddr().(*net.UDPAddr).Port, &received
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				data := make([]byte, 4096)
				for {
					n, err := conn.Read(data)
					if n > 0 {
						received.Add(int64(n))
						_, _ = conn.Write(data[:n])
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, &received
}

func TestTunnelFixedOutboundHotChangePreservesSiblingAndLedger(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			svc, tunnel, owner, _ := setupManagedActivationService(t)
			removeManagedTemplateOptIn(t, svc)
			directPort, directReceived := fixedOutboundEchoTarget(t, network)
			selectedPort, selectedReceived := fixedOutboundEchoTarget(t, network)
			settings := func(tag string) string {
				return fmt.Sprintf(`{"allowedNetwork":%q,"rewriteAddress":"127.0.0.1","rewritePort":%d,"outboundTag":%q,"clients":[]}`, network, directPort, tag)
			}
			template, err := (&SettingService{}).GetXrayConfigTemplate()
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal([]byte(template), &config); err != nil {
				t.Fatal(err)
			}
			config["outbounds"] = append(config["outbounds"].([]any), map[string]any{"tag": "selected", "protocol": "freedom", "settings": map[string]any{"redirect": fmt.Sprintf("127.0.0.1:%d", selectedPort), "finalRules": []any{map[string]any{"action": "allow"}}}})
			config["routing"].(map[string]any)["rules"] = append(config["routing"].(map[string]any)["rules"].([]any), map[string]any{"type": "field", "inboundTag": []string{tunnel.Tag}, "outboundTag": "missing-route"})
			raw, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := (&XraySettingService{}).SaveXraySetting(string(raw)); err != nil {
				t.Fatal(err)
			}
			update := func(tag string) {
				t.Helper()
				request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"id": tunnel.Id, "enable": true, "settings": json.RawMessage(settings(tag))})
				request.Port = tunnel.Port
				request.Tag = tunnel.Tag
				updated, _, err := (&InboundService{}).UpdateInbound(request)
				if err != nil {
					t.Fatal(err)
				}
				*tunnel = *updated
			}
			update("selected")
			probe, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			siblingRequest := tunnelOwnerRequest(t, owner.StableID, map[string]any{"enable": true, "settings": json.RawMessage(settings(""))})
			siblingRequest.Port = probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			sibling, _, err := (&InboundService{}).AddInbound(siblingRequest)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			boot := process.TrafficDrainBootID()
			dial := func(port int) net.Conn {
				t.Helper()
				conn, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				return conn
			}
			check := func(payload, direct int64) {
				t.Helper()
				if currentXrayProcess() != process || !process.IsRunning() || boot == "" || process.TrafficDrainBootID() != boot {
					t.Fatal("changing one fixed selection restarted the core")
				}
				if _, _, err := svc.GetXrayTraffic(); err != nil {
					t.Fatal(err)
				}
				total := policyLedgerTotal(t, owner.StableID)
				if total.RawUpload != 100+payload || total.RawDownload != 200+payload || total.BilledBytes != 300+payload*4 || directReceived.Load() != direct || selectedReceived.Load() != 6 {
					t.Fatalf("fixed target/shared ledger changed: %+v direct=%d selected=%d", total, directReceived.Load(), selectedReceived.Load())
				}
			}
			flow, survivor := dial(tunnel.Port), dial(sibling.Port)
			managedActivationEcho(t, flow, "chosen")
			managedActivationEcho(t, survivor, "siblng")
			check(12, 6)
			update("direct")
			if network == "tcp" {
				_ = flow.SetReadDeadline(time.Now().Add(time.Second))
				var timeout net.Error
				if _, err := flow.Read(make([]byte, 1)); err == nil || errors.As(err, &timeout) && timeout.Timeout() {
					t.Fatalf("old selected TCP stream survived replacement: %v", err)
				}
			}
			managedActivationEcho(t, dial(tunnel.Port), "direct")
			managedActivationEcho(t, survivor, "alive!")
			payload, direct := int64(24), int64(18)
			if network == "udp" {
				managedActivationEcho(t, flow, "newsrc")
				payload, direct = 30, 24
			}
			check(payload, direct)
			update("")
			denied := dial(tunnel.Port)
			_ = denied.SetDeadline(time.Now().Add(200 * time.Millisecond))
			_, _ = denied.Write([]byte("denied"))
			var timeout net.Error
			if _, err := denied.Read(make([]byte, 6)); err == nil || network == "tcp" && errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatalf("clearing fixed selection did not restore routing: %v", err)
			}
			managedActivationEcho(t, survivor, "alive!")
			check(payload+6, direct+6)
			var stored struct{ Settings string }
			if err := database.GetDB().Table("inbounds").Select("settings").Where("id = ?", tunnel.Id).Take(&stored).Error; err != nil {
				t.Fatal(err)
			}
			var final map[string]any
			if err := json.Unmarshal([]byte(stored.Settings), &final); err != nil || final["outboundTag"] != "" {
				t.Fatalf("routing choice did not persist: %s %v", stored.Settings, err)
			}
		})
	}
}
