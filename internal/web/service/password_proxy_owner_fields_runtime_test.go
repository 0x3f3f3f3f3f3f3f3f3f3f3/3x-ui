package service

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerFieldsRealCore(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			svc, tunnel, owner, _ := setupManagedActivationService(t)
			removeManagedTemplateOptIn(t, svc)
			other := passwordOwner(t, "fields-runtime-second")
			// Existing legacy billing is above one GiB. Starting unlimited
			// lets the public integer-GiB setter prove quota revocation without
			// generating a GiB of artificial traffic in the test.
			const historicalBilled int64 = 1073742124
			if err := database.GetDB().Model(owner).Update("total_gb", 0).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", owner.Email).Updates(map[string]any{"down": int64(1073742024), "total": 0}).Error; err != nil {
				t.Fatal(err)
			}
			target, received := passwordRemovalTarget(t, true)
			targetPort := target.Addr().(*net.TCPAddr).Port
			if err := database.GetDB().Model(tunnel).Update("settings", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp,udp","clients":[]}`, targetPort)).Error; err != nil {
				t.Fatal(err)
			}
			probe, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			inbounds, clients := &InboundService{}, &ClientService{}
			password, _, err := inbounds.AddInbound(&model.Inbound{Enable: true, Protocol: protocol, Listen: "127.0.0.1", Port: port, Settings: passwordOwnerSettings(t, protocol,
				map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": owner.StableID},
				map[string]any{"user": "ALICE", "pass": "alias-secret", "ownerClientId": owner.StableID},
				map[string]any{"user": "bob", "pass": "other-secret", "ownerClientId": other.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			if current, err := clients.GetByID(owner.Id); err != nil || !current.Enable || current.TotalGB != 0 {
				t.Fatalf("unlimited migration fixture drifted before bootstrap: %+v/%v", current, err)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process, mode := currentXrayProcess(), "http"
			boot := process.TrafficDrainBootID()
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
			dialTunnel := func(network string) net.Conn {
				t.Helper()
				flow, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = flow.Close() })
				return flow
			}
			assertRestricted := func(reason string) {
				t.Helper()
				flow, err := passwordConfigDial(mode, port, target.Addr().String(), "alice", "resource-secret")
				if err != nil {
					return
				}
				defer flow.Close()
				_ = flow.SetDeadline(time.Now().Add(2 * time.Second))
				_, _ = flow.Write([]byte("hello!"))
				if n, err := flow.Read(make([]byte, 6)); n != 0 || err == nil {
					t.Fatalf("enable cleared %s: bytes=%d error=%v", reason, n, err)
				}
			}
			first, alias, sibling := dial("alice", "resource-secret"), dial("ALICE", "alias-secret"), dial("bob", "other-secret")
			tcp, udp := dialTunnel("tcp4"), dialTunnel("udp4")
			echo := func(flows ...net.Conn) {
				t.Helper()
				for _, flow := range flows {
					managedActivationEcho(t, flow, "hello!")
				}
			}
			echo(first, alias, tcp, udp, sibling)
			var idle net.Conn
			var activeUDP, idleUDP, survivingUDP *passwordHotUDP
			if protocol == model.Mixed {
				idle = passwordHotAuthenticate(t, port, "alice", "resource-secret")
				activeUDP = passwordHotAssociate(t, port, "alice", "resource-secret")
				idleUDP = passwordHotAssociate(t, port, "ALICE", "alias-secret")
				survivingUDP = passwordHotAssociate(t, port, "bob", "other-secret")
				passwordHotUDPEcho(t, activeUDP, targetPort)
				passwordHotUDPEcho(t, survivingUDP, targetPort)
			}
			assertUsage := func(a, b int64) {
				t.Helper()
				if _, _, err := svc.GetXrayTraffic(); err != nil {
					t.Fatal(err)
				}
				aTotal, bTotal := policyLedgerTotal(t, owner.StableID), policyLedgerTotal(t, other.StableID)
				if received.Load() != a+b || aTotal.RawUpload != 100+a || aTotal.RawDownload != 1073742024+a || aTotal.BilledBytes != historicalBilled+4*a || bTotal.RawUpload != b || bTotal.RawDownload != b || bTotal.BilledBytes != 3*b {
					t.Fatalf("field target/owner conservation: target=%d A=%+v B=%+v expected=%d/%d", received.Load(), aTotal, bTotal, a, b)
				}
			}
			aBytes, bBytes := int64(24), int64(6)
			if protocol == model.Mixed {
				aBytes, bBytes = 30, 12
			}
			assertUsage(aBytes, bBytes)
			bulk := func(enable bool) {
				t.Helper()
				result, restart, err := clients.BulkSetEnable(inbounds, []string{owner.Email, owner.Email, "missing-runtime-owner"}, enable)
				if err != nil || restart || result.Changed != 1 || len(result.Skipped) != 1 || result.Skipped[0].Email != "missing-runtime-owner" {
					t.Fatalf("real bulk field contract=%+v/%v/%v", result, restart, err)
				}
				if currentXrayProcess() != process || !process.IsRunning() || process.TrafficDrainBootID() != boot {
					t.Fatal("field mutation replaced core boot")
				}
			}
			bulk(false)
			current, err := clients.GetByID(owner.Id)
			if err != nil || current.Enable {
				t.Fatalf("bulk disable did not save canonical state: %+v/%v", current, err)
			}
			for index, flow := range []net.Conn{first, alias, tcp} {
				t.Logf("checking revoked stream %d at %s", index, flow.RemoteAddr())
				managedActivationClosed(t, flow)
			}
			if protocol == model.Mixed {
				managedActivationClosed(t, idle)
				passwordHotUDPClosed(t, activeUDP)
				passwordHotUDPClosed(t, idleUDP)
				passwordHotUDPEcho(t, survivingUDP, targetPort)
				bBytes += 6
			}
			_ = udp.SetDeadline(time.Now().Add(150 * time.Millisecond))
			_, _ = udp.Write([]byte("hello!"))
			if _, err := udp.Read(make([]byte, 6)); err == nil {
				t.Fatal("disabled owner retained Tunnel UDP")
			}
			for _, credential := range [][2]string{{"alice", "resource-secret"}, {"ALICE", "alias-secret"}} {
				if denied, err := passwordConfigDial(mode, port, target.Addr().String(), credential[0], credential[1]); err == nil {
					_ = denied.Close()
					t.Fatal("disabled alias authenticated")
				}
			}
			echo(sibling)
			bBytes += 6
			assertUsage(aBytes, bBytes)
			bulk(true)
			first, tcp, udp = dial("alice", "resource-secret"), dialTunnel("tcp4"), dialTunnel("udp4")
			echo(first, tcp, udp, sibling)
			aBytes += 18
			bBytes += 6
			assertUsage(aBytes, bBytes)
			if _, err := clients.ResetClientTrafficLimitByEmail(inbounds, owner.Email, 1); err != nil {
				t.Fatal(err)
			}
			if current, err := clients.GetByID(owner.Id); err != nil || current.TotalGB != 1073741824 {
				t.Fatalf("GiB field setter did not save quota: %+v/%v", current, err)
			}
			managedActivationClosed(t, first)
			managedActivationClosed(t, tcp)
			echo(sibling)
			bBytes += 6
			assertUsage(aBytes, bBytes)
			// Enable cannot override a separate quota restriction.
			bulk(true)
			assertRestricted("exhausted quota")
			assertUsage(aBytes, bBytes)
			if _, err := clients.ResetClientTrafficLimitByEmail(inbounds, owner.Email, 0); err != nil {
				t.Fatal(err)
			}
			first, tcp, udp = dial("alice", "resource-secret"), dialTunnel("tcp4"), dialTunnel("udp4")
			echo(first, tcp, udp)
			aBytes += 18
			assertUsage(aBytes, bBytes)
			if _, err := clients.ResetClientExpiryTimeByEmail(inbounds, owner.Email, time.Now().Add(-time.Second).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, first)
			managedActivationClosed(t, tcp)
			bulk(true)
			assertRestricted("expiry")
			echo(sibling)
			bBytes += 6
			assertUsage(aBytes, bBytes)
			if saved, err := inbounds.GetInbound(password.Id); err != nil || saved.Settings != password.Settings {
				t.Fatalf("field lifecycle changed resource accounts: %+v/%v", saved, err)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			assertUsage(aBytes, bBytes)
		})
	}
}
