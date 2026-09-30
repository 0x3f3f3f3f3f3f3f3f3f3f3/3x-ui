package service

import (
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func passwordRemovalTarget(t *testing.T, udp bool) (net.Listener, *atomic.Int64) {
	t.Helper()
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	received := &atomic.Int64{}
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				payload := make([]byte, 6)
				for {
					n, err := io.ReadFull(conn, payload)
					received.Add(int64(n))
					if err != nil {
						return
					}
					if _, err := conn.Write(payload); err != nil {
						return
					}
				}
			}()
		}
	}()
	if udp {
		packet, err := net.ListenPacket("udp4", target.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = packet.Close() })
		go func() {
			buffer := make([]byte, 128)
			for {
				n, peer, err := packet.ReadFrom(buffer)
				if err != nil {
					return
				}
				received.Add(int64(n))
				_, _ = packet.WriteTo(buffer[:n], peer)
			}
		}()
	}
	return target, received
}

func TestPasswordProxyOwnerRemovalRealCore(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		for _, action := range []string{"detach", "bulk-detach", "delete", "bulk-delete"} {
			t.Run(fmt.Sprintf("%s/%s", protocol, action), func(t *testing.T) {
				svc, tunnel, first, _ := setupManagedActivationService(t)
				removeManagedTemplateOptIn(t, svc)
				second := passwordOwner(t, "removal-runtime-second")
				target, received := passwordRemovalTarget(t, protocol == model.Mixed)
				targetPort := target.Addr().(*net.TCPAddr).Port
				if err := database.GetDB().Model(tunnel).Update("settings", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, targetPort)).Error; err != nil {
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
					map[string]any{"user": "alice", "pass": "first-resource", "ownerClientId": first.StableID},
					map[string]any{"user": "ALICE", "pass": "alias-resource", "ownerClientId": first.StableID},
					map[string]any{"user": "bob", "pass": "second-resource", "ownerClientId": second.StableID})})
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.RestartXray(true); err != nil {
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
					flow, err := passwordConfigDial(mode, ib.Port, target.Addr().String(), user, pass)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = flow.Close() })
					return flow
				}
				firstFlow, alias, sibling := dial("alice", "first-resource"), dial("ALICE", "alias-resource"), dial("bob", "second-resource")
				tunnelFlow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = tunnelFlow.Close() })
				for _, flow := range []net.Conn{firstFlow, alias, sibling, tunnelFlow} {
					managedActivationEcho(t, flow, "hello!")
				}
				var idle net.Conn
				var firstUDP, idleUDP, siblingUDP *passwordHotUDP
				if protocol == model.Mixed {
					idle = passwordHotAuthenticate(t, ib.Port, "alice", "first-resource")
					firstUDP = passwordHotAssociate(t, ib.Port, "alice", "first-resource")
					idleUDP = passwordHotAssociate(t, ib.Port, "ALICE", "alias-resource")
					siblingUDP = passwordHotAssociate(t, ib.Port, "bob", "second-resource")
					passwordHotUDPEcho(t, firstUDP, targetPort)
					passwordHotUDPEcho(t, siblingUDP, targetPort)
				}
				deleting := action == "delete" || action == "bulk-delete"
				keepTraffic := action != "bulk-delete"
				switch action {
				case "detach":
					_, err = clients.Detach(inbounds, first.Id, []int{ib.Id})
				case "bulk-detach":
					var result *BulkDetachResult
					result, _, err = clients.BulkDetach(inbounds, []string{first.Email}, []int{ib.Id})
					if err == nil && (len(result.Detached) != 1 || len(result.Errors) != 0) {
						t.Fatalf("real bulk detach result: %+v", result)
					}
				case "delete":
					_, err = clients.Delete(inbounds, first.Id, keepTraffic)
				case "bulk-delete":
					var result BulkDeleteResult
					result, _, err = clients.BulkDelete(inbounds, []string{first.Email}, keepTraffic)
					if err == nil && (result.Deleted != 1 || len(result.Skipped) != 0) {
						t.Fatalf("real bulk delete result: %+v", result)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if currentXrayProcess() != process || !process.IsRunning() || boot == "" || process.TrafficDrainBootID() != boot {
					t.Fatal("owner removal replaced the core boot")
				}
				managedActivationClosed(t, firstFlow)
				managedActivationClosed(t, alias)
				if protocol == model.Mixed {
					managedActivationClosed(t, idle)
					passwordHotUDPClosed(t, firstUDP)
					passwordHotUDPClosed(t, idleUDP)
					passwordHotUDPEcho(t, siblingUDP, targetPort)
				}
				managedActivationEcho(t, sibling, "hello!")
				if deleting {
					managedActivationClosed(t, tunnelFlow)
				} else {
					managedActivationEcho(t, tunnelFlow, "hello!")
				}
				for _, credential := range [][2]string{{"alice", "first-resource"}, {"ALICE", "alias-resource"}} {
					if flow, err := passwordConfigDial(mode, ib.Port, target.Addr().String(), credential[0], credential[1]); err == nil {
						_ = flow.Close()
						t.Fatal("removed credential still authenticates")
					}
				}
				if _, _, err := svc.GetXrayTraffic(); err != nil {
					t.Fatal(err)
				}
				expectedA, expectedB, expectedTarget := int64(24), int64(12), int64(36)
				if deleting {
					expectedA, expectedTarget = 18, 30
				}
				if protocol == model.Mixed {
					expectedA += 6
					expectedB += 12
					expectedTarget += 18
				}
				assertUsage := func() {
					t.Helper()
					a, b := policyLedgerTotal(t, first.StableID), policyLedgerTotal(t, second.StableID)
					if a.RawUpload != 100+expectedA || a.RawDownload != 200+expectedA || a.BilledBytes != 300+4*expectedA || b.RawUpload != expectedB || b.RawDownload != expectedB || b.BilledBytes != 3*expectedB || received.Load() != expectedTarget {
						t.Fatalf("target/owner conservation after %s: target=%d expected=%d A=%+v B=%+v", action, received.Load(), expectedTarget, a, b)
					}
				}
				assertUsage()
				if deleting {
					var count int64
					if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", first.StableID).Count(&count).Error; err != nil || count != 0 {
						t.Fatalf("deleted canonical owner remains: %d %v", count, err)
					}
					if !keepTraffic {
						if err := database.GetDB().Table("client_traffics").Where("email = ?", first.Email).Count(&count).Error; err != nil || count != 0 {
							t.Fatalf("delete traffic flag ignored: %d %v", count, err)
						}
					}
					if err := svc.RestartXray(true); err != nil {
						t.Fatal(err)
					}
					if _, _, err := svc.GetXrayTraffic(); err != nil {
						t.Fatal(err)
					}
					assertUsage()
				}
				if _, err := clients.Detach(inbounds, second.Id, []int{ib.Id}); err != nil {
					t.Fatal(err)
				}
				if flow, err := passwordConfigDial("http", ib.Port, target.Addr().String(), "", ""); err == nil {
					_ = flow.Close()
					t.Fatal("last owner removal restored anonymous HTTP")
				}
				if received.Load() != expectedTarget {
					t.Fatal("empty authenticated listener leaked target payload")
				}
			})
		}
	}
}
