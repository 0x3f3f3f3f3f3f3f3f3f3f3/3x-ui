package service

import (
	"fmt"
	"net"
	"testing"
	"time"

	S "github.com/sagernet/sing-snell"
	"github.com/sagernet/sing-snell/snellv4"
	"github.com/sagernet/sing-snell/snellv6"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func nativeSnellPanelMethod(t *testing.T, version int, psk string) S.Method {
	t.Helper()
	if version == 6 {
		client, err := snellv6.NewClient(snellv6.ClientOptions{PSK: []byte(psk)})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	client, err := snellv4.NewClient(snellv4.ClientOptions{PSK: []byte(psk)})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func nativeSnellPanelFlow(t *testing.T, version int, listener *model.Inbound, psk string, target int) net.Conn {
	t.Helper()
	raw, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listener.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := nativeSnellPanelMethod(t, version, psk).DialConn(raw, M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", target)))
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flow.Close() })
	return flow
}

func nativeSnellPanelFreePort(t *testing.T) int {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	return port
}

func TestSnellPanelPublicCRUDRealCorePreservesSiblingAndSharedLedger(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			svc, tunnel, owner, target := setupManagedActivationService(t)
			removeManagedTemplateOptIn(t, svc)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process, boot := currentXrayProcess(), currentXrayProcess().TrafficDrainBootID()
			inbounds, clients := &InboundService{}, &ClientService{}
			listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, Listen: "127.0.0.1", Port: nativeSnellPanelFreePort(t), Enable: true, OwnerClientID: &owner.StableID, Settings: fmt.Sprintf(`{"version":%d,"clients":[]}`, version)})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := clients.GetByID(owner.Id)
			if err != nil {
				t.Fatal(err)
			}
			sibling, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, Listen: "127.0.0.1", Port: nativeSnellPanelFreePort(t), Enable: true, Settings: fmt.Sprintf(`{"version":%d,"clients":[{"email":"snell-sibling","enable":true,"snellPsk":"sibling-native-psk"}]}`, version)})
			if err != nil {
				t.Fatal(err)
			}
			first := nativeSnellPanelFlow(t, version, listener, stored.SnellPSK, target)
			other := nativeSnellPanelFlow(t, version, sibling, "sibling-native-psk", target)
			shared, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = shared.Close() })
			managedActivationEcho(t, first, "first")
			managedActivationEcho(t, other, "other")
			managedActivationEcho(t, shared, "shared")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			before := policyLedgerTotal(t, owner.StableID)
			if before.RawUpload != 111 || before.RawDownload != 211 || before.BilledBytes != 344 {
				t.Fatalf("Snell and Tunnel shared ledger mismatch: %+v", before)
			}
			rotate := model.Client{Email: owner.Email, Enable: true, SnellPSK: "rotated-native-psk"}
			if _, err := clients.Update(inbounds, owner.Id, rotate, 0); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, first)
			stale := nativeSnellPanelFlow(t, version, listener, stored.SnellPSK, target)
			_ = stale.SetDeadline(time.Now().Add(time.Second))
			_, writeErr := stale.Write([]byte("revoked"))
			if writeErr == nil {
				if _, readErr := stale.Read(make([]byte, 16)); readErr == nil {
					t.Fatal("revoked native PSK authenticated")
				}
			}
			_ = stale.Close()
			current := nativeSnellPanelFlow(t, version, listener, rotate.SnellPSK, target)
			managedActivationEcho(t, current, "fresh")
			managedActivationEcho(t, other, "alive")
			managedActivationEcho(t, shared, "stay")
			if currentXrayProcess() != process || currentXrayProcess().TrafficDrainBootID() != boot {
				t.Fatal("native PSK rotation restarted unrelated listeners")
			}
			rotate.Enable = false
			if _, err := clients.Update(inbounds, owner.Id, rotate, 0); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, current)
			managedActivationClosed(t, shared)
			managedActivationEcho(t, other, "survive")
			rotate.Enable = true
			if _, err := clients.Update(inbounds, owner.Id, rotate, 0); err != nil {
				t.Fatal(err)
			}
			current = nativeSnellPanelFlow(t, version, listener, rotate.SnellPSK, target)
			managedActivationEcho(t, current, "enabled")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			after := policyLedgerTotal(t, owner.StableID)
			if after.RawUpload != 127 || after.RawDownload != 227 || after.BilledBytes != 408 {
				t.Fatalf("Snell lifecycle changed historical usage: %+v", after)
			}
			if _, err := clients.Detach(inbounds, owner.Id, []int{listener.Id}); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, current)
			managedActivationEcho(t, other, "after-detach")
			saved, err := inbounds.GetInbound(listener.Id)
			if err != nil || saved.Enable {
				t.Fatal("empty listener retained SQL activation")
			}
			tcp, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listener.Port))
			if err != nil {
				t.Fatalf("last detach retained TCP listener: %v", err)
			}
			_ = tcp.Close()
			if version == 5 {
				udp, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", listener.Port))
				if err != nil {
					t.Fatalf("last detach retained UDP listener: %v", err)
				}
				_ = udp.Close()
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			reopened := nativeSnellPanelFlow(t, version, sibling, "sibling-native-psk", target)
			managedActivationEcho(t, reopened, "restart")
			got, err := clients.GetByID(owner.Id)
			if err != nil || got.StableID != owner.StableID || got.SnellPSK != rotate.SnellPSK {
				t.Fatal("restart or detach changed canonical native identity")
			}
			var memberships int64
			if err := database.GetDB().Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", owner.Id, listener.Id).Count(&memberships).Error; err != nil || memberships != 0 {
				t.Fatal("last detach retained ownership")
			}
		})
	}
}

func TestSnellPanelExpiryQuotaAndFinalDeleteRealCore(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			svc, _, owner, target := setupManagedActivationService(t)
			removeManagedTemplateOptIn(t, svc)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			inbounds, clients := &InboundService{}, &ClientService{}
			listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, Listen: "127.0.0.1", Port: nativeSnellPanelFreePort(t), Enable: true, OwnerClientID: &owner.StableID, Settings: fmt.Sprintf(`{"version":%d,"clients":[]}`, version)})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := clients.GetByID(owner.Id)
			if err != nil {
				t.Fatal(err)
			}
			flow := nativeSnellPanelFlow(t, version, listener, stored.SnellPSK, target)
			managedActivationEcho(t, flow, "before")
			update := *stored.ToClient()
			update.ExpiryTime = time.Now().Add(-time.Second).UnixMilli()
			if _, err := clients.Update(inbounds, owner.Id, update, 0); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, flow)
			update.ExpiryTime = 0
			if _, err := clients.Update(inbounds, owner.Id, update, 0); err != nil {
				t.Fatal(err)
			}
			flow = nativeSnellPanelFlow(t, version, listener, stored.SnellPSK, target)
			managedActivationEcho(t, flow, "restored")
			update.TotalGB = 1
			if _, err := clients.Update(inbounds, owner.Id, update, 0); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, flow)
			update.TotalGB = 10000
			if _, err := clients.Update(inbounds, owner.Id, update, 0); err != nil {
				t.Fatal(err)
			}
			flow = nativeSnellPanelFlow(t, version, listener, stored.SnellPSK, target)
			managedActivationEcho(t, flow, "quota-restored")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			before := policyLedgerTotal(t, owner.StableID)
			if before.RawUpload != 128 || before.RawDownload != 228 || before.BilledBytes != 412 {
				t.Fatalf("expiry/quota repriced historical usage: %+v", before)
			}
			if _, err := clients.Delete(inbounds, owner.Id, true); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, flow)
			saved, err := inbounds.GetInbound(listener.Id)
			if err != nil || saved.Enable {
				t.Fatal("deleted owner retained enabled native resource")
			}
			tcp, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listener.Port))
			if err != nil {
				t.Fatalf("final delete retained TCP listener: %v", err)
			}
			_ = tcp.Close()
			if version == 5 {
				udp, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", listener.Port))
				if err != nil {
					t.Fatalf("final delete retained UDP listener: %v", err)
				}
				_ = udp.Close()
			}
			after := policyLedgerTotal(t, owner.StableID)
			if after.RawUpload != before.RawUpload || after.RawDownload != before.RawDownload || after.BilledBytes != before.BilledBytes {
				t.Fatal("account deletion erased retained native ledger")
			}
		})
	}
}
