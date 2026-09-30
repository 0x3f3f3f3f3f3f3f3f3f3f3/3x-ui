package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestPasswordProxyHotChangesPreserveSiblingAndLedger(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			svc, tunnel, first, _ := setupManagedActivationService(t)
			removeManagedTemplateOptIn(t, svc)
			second := passwordOwner(t, "hot-password-second")
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = target.Close() })
			var received atomic.Int64
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
			targetPort := target.Addr().(*net.TCPAddr).Port
			var udpTarget net.PacketConn
			if protocol == model.Mixed {
				udpTarget, err = net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", targetPort))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = udpTarget.Close() })
				go func() {
					payload := make([]byte, 128)
					for {
						n, peer, err := udpTarget.ReadFrom(payload)
						if err != nil {
							return
						}
						received.Add(int64(n))
						_, _ = udpTarget.WriteTo(payload[:n], peer)
					}
				}()
			}
			if err := database.GetDB().Model(tunnel).Update("settings", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, target.Addr().(*net.TCPAddr).Port)).Error; err != nil {
				t.Fatal(err)
			}
			probe, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			accounts := func(password string, includeA, includeB bool) []map[string]any {
				out := []map[string]any{}
				if includeA {
					out = append(out, map[string]any{"user": "alice", "pass": password, "ownerClientId": first.StableID}, map[string]any{"user": "ALICE", "pass": "alias", "ownerClientId": first.StableID})
				}
				if includeB {
					out = append(out, map[string]any{"user": "bob", "pass": "other", "ownerClientId": second.StableID})
				}
				return out
			}
			settings := func(password string, includeA, includeB bool) string {
				out := map[string]any{"accounts": accounts(password, includeA, includeB), "userLevel": 7}
				if protocol == model.Mixed {
					out["auth"], out["udp"] = "password", true
				}
				raw, err := json.Marshal(out)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Enable: true, Protocol: protocol, Listen: "127.0.0.1", Port: port, Settings: settings("old", true, true)})
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			boot := process.TrafficDrainBootID()
			dial := func(user, password string) net.Conn {
				t.Helper()
				mode := "http"
				if protocol == model.Mixed {
					mode = "socks"
				}
				conn, err := passwordConfigDial(mode, ib.Port, target.Addr().String(), user, password)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				return conn
			}
			old, alias, sibling := dial("alice", "old"), dial("ALICE", "alias"), dial("bob", "other")
			tunnelFlow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tunnelFlow.Close() })
			for _, flow := range []net.Conn{old, alias, sibling, tunnelFlow} {
				managedActivationEcho(t, flow, "hello!")
			}
			var fallback, siblingHTTP, idle net.Conn
			var oldUDP, idleUDP, siblingUDP, freshUDP *passwordHotUDP
			if protocol == model.Mixed {
				fallback, err = passwordConfigDial("http", ib.Port, target.Addr().String(), "alice", "old")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = fallback.Close() })
				siblingHTTP, err = passwordConfigDial("http", ib.Port, target.Addr().String(), "bob", "other")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = siblingHTTP.Close() })
				managedActivationEcho(t, fallback, "hello!")
				managedActivationEcho(t, siblingHTTP, "hello!")
				idle = passwordHotAuthenticate(t, ib.Port, "alice", "old")
				oldUDP = passwordHotAssociate(t, ib.Port, "alice", "old")
				idleUDP = passwordHotAssociate(t, ib.Port, "ALICE", "alias")
				siblingUDP = passwordHotAssociate(t, ib.Port, "bob", "other")
				passwordHotUDPEcho(t, oldUDP, targetPort)
				passwordHotUDPEcho(t, siblingUDP, targetPort)
			}
			save := func(password string, includeA, includeB bool) {
				t.Helper()
				request := *ib
				request.Settings = settings(password, includeA, includeB)
				if _, _, err := (&InboundService{}).UpdateInbound(&request); err != nil {
					t.Fatal(err)
				}
				if currentXrayProcess() != process || !process.IsRunning() || boot == "" || process.TrafficDrainBootID() != boot {
					t.Fatal("credential edit replaced the core boot")
				}
			}
			save("new", true, true)
			managedActivationClosed(t, old)
			managedActivationClosed(t, alias)
			if protocol == model.Mixed {
				managedActivationClosed(t, fallback)
				managedActivationClosed(t, idle)
				passwordHotUDPClosed(t, oldUDP)
				passwordHotUDPClosed(t, idleUDP)
				managedActivationEcho(t, siblingHTTP, "hello!")
				passwordHotUDPEcho(t, siblingUDP, targetPort)
			}
			managedActivationEcho(t, sibling, "hello!")
			managedActivationEcho(t, tunnelFlow, "hello!")
			mode := "http"
			if protocol == model.Mixed {
				mode = "socks"
			}
			if conn, err := passwordConfigDial(mode, ib.Port, target.Addr().String(), "alice", "old"); err == nil {
				_ = conn.Close()
				t.Fatal("rotated credential still authenticates")
			}
			fresh, freshAlias := dial("alice", "new"), dial("ALICE", "alias")
			managedActivationEcho(t, fresh, "hello!")
			managedActivationEcho(t, freshAlias, "hello!")
			if protocol == model.Mixed {
				freshUDP = passwordHotAssociate(t, ib.Port, "alice", "new")
				passwordHotUDPEcho(t, freshUDP, targetPort)
			}
			save("new", false, true)
			managedActivationClosed(t, fresh)
			managedActivationClosed(t, freshAlias)
			if protocol == model.Mixed {
				passwordHotUDPClosed(t, freshUDP)
				managedActivationEcho(t, siblingHTTP, "hello!")
				passwordHotUDPEcho(t, siblingUDP, targetPort)
			}
			managedActivationEcho(t, sibling, "hello!")
			managedActivationEcho(t, tunnelFlow, "hello!")
			save("new", false, false)
			managedActivationClosed(t, sibling)
			if protocol == model.Mixed {
				managedActivationClosed(t, siblingHTTP)
				passwordHotUDPClosed(t, siblingUDP)
			}
			managedActivationEcho(t, tunnelFlow, "hello!")
			if conn, err := passwordConfigDial(mode, ib.Port, target.Addr().String(), "", ""); err == nil {
				_ = conn.Close()
				t.Fatal("empty listener allowed anonymous traffic")
			}
			wantTarget, wantA, wantB, wantABilled, wantBBilled := int64(66), int64(48), int64(18), int64(492), int64(54)
			if protocol == model.Mixed {
				wantTarget, wantA, wantB, wantABilled, wantBBilled = 120, 66, 54, 564, 162
			}
			if received.Load() != wantTarget {
				t.Fatalf("independent target received %d bytes, want %d", received.Load(), wantTarget)
			}
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			if total := policyLedgerTotal(t, first.StableID); total.RawUpload != 100+wantA || total.RawDownload != 200+wantA || total.BilledBytes != wantABilled {
				t.Fatalf("changed group/Tunnel lost shared history: %+v", total)
			}
			if total := policyLedgerTotal(t, second.StableID); total.RawUpload != wantB || total.RawDownload != wantB || total.BilledBytes != wantBBilled {
				t.Fatalf("sibling ledger changed: %+v", total)
			}
		})
	}
}
