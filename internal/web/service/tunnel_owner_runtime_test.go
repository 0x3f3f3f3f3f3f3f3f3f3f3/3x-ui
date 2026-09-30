//go:build linux

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestTunnelOwnerSelectionReassignsLiveTCPAndUDP(t *testing.T) {
	svc, tunnel, first, targetPort := setupManagedActivationService(t)
	removeManagedTemplateOptIn(t, svc)
	db := database.GetDB()
	udpTarget, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", targetPort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udpTarget.Close() })
	go func() {
		buf := make([]byte, 1024)
		for {
			n, peer, err := udpTarget.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udpTarget.WriteTo(buf[:n], peer)
		}
	}()
	settings := fmt.Sprintf(`{"rewriteAddress":"127.0.0.1","rewritePort":%d,"allowedNetwork":"tcp,udp","clients":[]}`, targetPort)
	if err := db.Model(tunnel).Update("settings", settings).Error; err != nil {
		t.Fatal(err)
	}
	second := &model.ClientRecord{Email: "next-tunnel-owner", SubID: "next-tunnel-owner", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "3"}}
	if err := db.Create(second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: second.Email, Up: 10, Down: 20, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	siblingRequest := tunnelOwnerRequest(t, first.StableID, map[string]any{"enable": true, "settings": json.RawMessage(settings)})
	siblingRequest.Port = probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	sibling, _, err := (&InboundService{}).AddInbound(siblingRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	dial := func(network string, port int) net.Conn {
		t.Helper()
		flow, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = flow.Close() })
		return flow
	}
	oldTCP, oldUDP := dial("tcp", tunnel.Port), dial("udp", tunnel.Port)
	siblingTCP := dial("tcp", sibling.Port)
	managedActivationEcho(t, oldTCP, "old-tcp")
	managedActivationEcho(t, oldUDP, "old-udp")
	managedActivationEcho(t, siblingTCP, "sibling")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	before := policyLedgerTotal(t, first.StableID)
	if before.RawUpload != 121 || before.RawDownload != 221 || before.BilledBytes != 384 {
		t.Fatalf("initial shared traffic: %+v", before)
	}
	request := tunnelOwnerRequest(t, second.StableID, map[string]any{"id": tunnel.Id, "enable": true, "settings": json.RawMessage(settings)})
	request.Port = tunnel.Port
	if _, _, err := (&InboundService{}).UpdateInbound(request); err != nil {
		t.Fatal(err)
	}
	_ = oldTCP.SetReadDeadline(time.Now().Add(2 * time.Second))
	var one [1]byte
	var timeout net.Error
	if _, err := oldTCP.Read(one[:]); err == nil {
		t.Fatal("reassigned TCP flow remained open")
	} else if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("reassigned TCP flow was not closed")
	}
	managedActivationEcho(t, dial("tcp", tunnel.Port), "new-tcp")
	managedActivationEcho(t, oldUDP, "new-udp")
	managedActivationEcho(t, siblingTCP, "still")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	oldTotal, newTotal := policyLedgerTotal(t, first.StableID), policyLedgerTotal(t, second.StableID)
	if oldTotal.RawUpload != 126 || oldTotal.RawDownload != 226 || oldTotal.BilledBytes != 404 {
		t.Fatalf("former owner lost history or was billed new-owner traffic: %+v", oldTotal)
	}
	if newTotal.RawUpload != 24 || newTotal.RawDownload != 34 || newTotal.BilledBytes != 114 {
		t.Fatalf("replacement owner did not receive independent TCP/UDP billing: %+v", newTotal)
	}
}
