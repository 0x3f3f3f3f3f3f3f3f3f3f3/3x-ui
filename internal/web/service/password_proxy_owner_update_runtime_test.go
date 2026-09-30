package service

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestPasswordProxyOwnerSingleUpdateRealCore(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			xraySvc, tunnel, first, _ := setupManagedActivationService(t)
			removeManagedTemplateOptIn(t, xraySvc)
			second := passwordOwner(t, "update-runtime-second")
			target, received := passwordRemovalTarget(t, false)
			if err := database.GetDB().Model(tunnel).Update("settings", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, target.Addr().(*net.TCPAddr).Port)).Error; err != nil {
				t.Fatal(err)
			}
			probe, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			inbounds, clients := &InboundService{}, &ClientService{}
			ib, _, err := inbounds.AddInbound(&model.Inbound{Enable: true, Protocol: protocol, Listen: "127.0.0.1", Port: port, Settings: passwordOwnerSettings(t, protocol,
				map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": first.StableID},
				map[string]any{"user": "ALICE", "pass": "alias-secret", "ownerClientId": first.StableID},
				map[string]any{"user": "bob", "pass": "other-secret", "ownerClientId": second.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			if err := xraySvc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			boot := process.TrafficDrainBootID()
			mode := "http"
			if protocol == model.Mixed {
				mode = "socks"
			}
			dial := func(user, pass string) net.Conn {
				t.Helper()
				flow, err := passwordConfigDial(mode, port, target.Addr().String(), user, pass)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = flow.Close() })
				return flow
			}
			dialTunnel := func() net.Conn {
				t.Helper()
				flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = flow.Close() })
				return flow
			}
			firstFlow, alias, sibling, tunnelFlow := dial("alice", "resource-secret"), dial("ALICE", "alias-secret"), dial("bob", "other-secret"), dialTunnel()
			echo := func(flows ...net.Conn) {
				t.Helper()
				for _, flow := range flows {
					managedActivationEcho(t, flow, "hello!")
				}
			}
			echo(firstFlow, alias, tunnelFlow, sibling)
			update := func(mutate func(*model.Client)) {
				t.Helper()
				current, err := clients.GetByID(first.Id)
				if err != nil {
					t.Fatal(err)
				}
				requested := *current.ToClient()
				mutate(&requested)
				if _, err := clients.Update(inbounds, first.Id, requested, current.LimitHwid); err != nil {
					t.Fatal(err)
				}
				if currentXrayProcess() != process || !process.IsRunning() || process.TrafficDrainBootID() != boot {
					t.Fatal("shared update replaced the core boot")
				}
			}
			assertUsage := func(a, b, billed int64) {
				t.Helper()
				if _, _, err := xraySvc.GetXrayTraffic(); err != nil {
					t.Fatal(err)
				}
				aTotal, bTotal := policyLedgerTotal(t, first.StableID), policyLedgerTotal(t, second.StableID)
				if received.Load() != a+b || aTotal.RawUpload != 100+a || aTotal.RawDownload != 200+a || aTotal.BilledBytes != billed || bTotal.RawUpload != b || bTotal.RawDownload != b || bTotal.BilledBytes != 3*b {
					t.Fatalf("target/owner conservation: target=%d A=%+v B=%+v expected=%d/%d/%d", received.Load(), aTotal, bTotal, a, b, billed)
				}
			}
			assertUsage(18, 6, 372)
			update(func(c *model.Client) {
				c.Policy = &model.ClientPolicyOptions{UploadBytesPerSecond: 65536, DownloadBytesPerSecond: 131072, Multiplier: "0.5"}
			})
			var config conf.ClientPolicyConfig
			if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
				t.Fatal(err)
			}
			foundRates := false
			for _, policy := range config.Policies {
				if policy.ClientID == first.StableID {
					foundRates = policy.UploadRate == 65536 && policy.DownloadRate == 131072
				}
			}
			if !foundRates {
				t.Fatal("public update did not acknowledge directional rates")
			}
			echo(firstFlow, alias, tunnelFlow, sibling)
			assertUsage(36, 12, 390)
			update(func(c *model.Client) {
				c.Email, c.Password, c.ID = "update-runtime-renamed", "canonical-rotated", uuid.NewString()
			})
			managedActivationClosed(t, firstFlow)
			managedActivationClosed(t, alias)
			// Canonical email changes the Tunnel listener's accounting label.
			// Its replacement closes this owner's flow, while B stays live.
			managedActivationClosed(t, tunnelFlow)
			tunnelFlow = dialTunnel()
			firstFlow, alias = dial("alice", "resource-secret"), dial("ALICE", "alias-secret")
			if wrong, err := passwordConfigDial(mode, port, target.Addr().String(), "alice", "canonical-rotated"); err == nil {
				_ = wrong.Close()
				t.Fatal("shared password replaced the resource password")
			}
			echo(firstFlow, alias, tunnelFlow, sibling)
			assertUsage(54, 18, 408)
			update(func(c *model.Client) { c.Enable = false })
			for _, flow := range []net.Conn{firstFlow, alias, tunnelFlow} {
				managedActivationClosed(t, flow)
			}
			if denied, err := passwordConfigDial(mode, port, target.Addr().String(), "alice", "resource-secret"); err == nil {
				_ = denied.Close()
				t.Fatal("disabled owner reached the target")
			}
			echo(sibling)
			assertUsage(54, 24, 408)
			update(func(c *model.Client) { c.Enable = true })
			firstFlow, tunnelFlow = dial("alice", "resource-secret"), dialTunnel()
			echo(firstFlow, tunnelFlow, sibling)
			assertUsage(66, 30, 420)
			update(func(c *model.Client) { c.TotalGB = 419 })
			managedActivationClosed(t, firstFlow)
			managedActivationClosed(t, tunnelFlow)
			echo(sibling)
			assertUsage(66, 36, 420)
			update(func(c *model.Client) { c.TotalGB = 10000 })
			firstFlow, tunnelFlow = dial("alice", "resource-secret"), dialTunnel()
			echo(firstFlow, tunnelFlow)
			assertUsage(78, 36, 432)
			update(func(c *model.Client) { c.ExpiryTime = time.Now().Add(-time.Second).UnixMilli() })
			managedActivationClosed(t, firstFlow)
			managedActivationClosed(t, tunnelFlow)
			echo(sibling)
			assertUsage(78, 42, 432)
			var saved model.Inbound
			if err := database.GetDB().First(&saved, ib.Id).Error; err != nil || saved.Settings != ib.Settings {
				t.Fatalf("shared lifecycle rewrote resource accounts: %+v %v", saved, err)
			}
			if err := xraySvc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			assertUsage(78, 42, 432)
		})
	}
}
