//go:build linux

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestTunnelSourceACLHotNarrowingPreservesSiblingAndLedger(t *testing.T) {
	svc, tunnel, owner, targetPort := setupManagedActivationService(t)
	removeManagedTemplateOptIn(t, svc)
	db := database.GetDB()
	var packets atomic.Int32
	target, err := net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", targetPort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := target.ReadFrom(buf)
			if err != nil {
				return
			}
			packets.Add(1)
			_, _ = target.WriteTo(buf[:n], addr)
		}
	}()
	settings := func(prefix string) string {
		return fmt.Sprintf(`{"allowedNetwork":"tcp,udp","rewriteAddress":"127.0.0.1","rewritePort":%d,"allowedSourceCidrs":[%q],"clients":[]}`, targetPort, prefix)
	}
	request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"id": tunnel.Id, "enable": true, "settings": json.RawMessage(settings("127.0.0.0/8"))})
	request.Port = tunnel.Port
	if _, _, err := (&InboundService{}).UpdateInbound(request); err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	siblingRequest := tunnelOwnerRequest(t, owner.StableID, map[string]any{"enable": true, "settings": json.RawMessage(settings("127.0.0.1/32"))})
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
	if boot == "" {
		t.Fatal("missing real core boot identity")
	}
	dial := func(network, source string, port int) net.Conn {
		t.Helper()
		dialer := net.Dialer{Timeout: time.Second}
		if network == "tcp4" {
			dialer.LocalAddr = &net.TCPAddr{IP: net.ParseIP(source)}
		} else {
			dialer.LocalAddr = &net.UDPAddr{IP: net.ParseIP(source)}
		}
		conn, err := dialer.Dial(network, fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	tcp, udp := dial("tcp4", "127.0.0.1", tunnel.Port), dial("udp4", "127.0.0.1", tunnel.Port)
	survivor := dial("tcp4", "127.0.0.1", sibling.Port)
	for _, conn := range []net.Conn{tcp, udp, survivor} {
		managedActivationEcho(t, conn, "abcdef")
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	before := policyLedgerTotal(t, owner.StableID)
	if before.RawUpload != 118 || before.RawDownload != 218 || before.BilledBytes != 372 {
		t.Fatalf("initial shared ledger: %+v", before)
	}
	var stored model.Inbound
	if err := db.First(&stored, tunnel.Id).Error; err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{settings("127.0.0.1/33"), strings.ReplaceAll(settings("127.0.0.1/33"), "allowedSourceCidrs", "AllowedSourceCidrs")} {
		candidate := stored
		candidate.Settings = invalid
		if _, _, err := (&InboundService{}).UpdateInbound(&candidate); err == nil || !strings.Contains(strings.ToLower(err.Error()), "source") {
			t.Fatalf("invalid ACL update accepted: %v", err)
		}
		var after model.Inbound
		if err := db.First(&after, tunnel.Id).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored, after) {
			t.Fatal("invalid ACL changed saved config")
		}
	}
	managedActivationEcho(t, tcp, "abcdef")
	managedActivationEcho(t, survivor, "abcdef")
	request = tunnelOwnerRequest(t, owner.StableID, map[string]any{"id": tunnel.Id, "enable": true, "settings": json.RawMessage(settings("127.0.0.2/32"))})
	request.Port = tunnel.Port
	if _, _, err := (&InboundService{}).UpdateInbound(request); err != nil {
		t.Fatal(err)
	}
	if currentXrayProcess() != process || !process.IsRunning() || process.TrafficDrainBootID() != boot {
		t.Fatal("ACL update restarted the shared core")
	}
	_ = tcp.SetReadDeadline(time.Now().Add(2 * time.Second))
	var timeout net.Error
	if _, err := tcp.Read(make([]byte, 1)); err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("old TCP flow not closed: %v", err)
	}
	for _, denied := range []net.Conn{udp, dial("tcp4", "127.0.0.1", tunnel.Port), dial("udp4", "127.0.0.1", tunnel.Port)} {
		_ = denied.SetDeadline(time.Now().Add(200 * time.Millisecond))
		_, _ = denied.Write([]byte("must-not-arrive"))
		if _, err := denied.Read(make([]byte, 32)); err == nil {
			t.Fatal("denied source received data")
		}
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	afterDeny := policyLedgerTotal(t, owner.StableID)
	if afterDeny.RawUpload != 130 || afterDeny.RawDownload != 230 || afterDeny.BilledBytes != 420 || packets.Load() != 1 {
		t.Fatalf("denied source reached target or ledger: %+v packets=%d", afterDeny, packets.Load())
	}
	for _, conn := range []net.Conn{dial("tcp4", "127.0.0.2", tunnel.Port), dial("udp4", "127.0.0.2", tunnel.Port), survivor} {
		managedActivationEcho(t, conn, "abcdef")
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	final := policyLedgerTotal(t, owner.StableID)
	if final.RawUpload != 148 || final.RawDownload != 248 || final.BilledBytes != 492 || packets.Load() != 2 {
		t.Fatalf("narrowed source/sibling ledger: %+v packets=%d", final, packets.Load())
	}
}

func TestTunnelSourceACLDriftedOwnerDeletionReleasesLivePorts(t *testing.T) {
	svc, tunnel, owner, targetPort := setupManagedActivationService(t)
	db := database.GetDB()
	settings := fmt.Sprintf(`{"allowedNetwork":"tcp,udp","rewriteAddress":"127.0.0.1","rewritePort":%d,"allowedSourceCidrs":["127.0.0.1/32"],"clients":[]}`, targetPort)
	request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"id": tunnel.Id, "enable": true, "settings": json.RawMessage(settings)})
	request.Port = tunnel.Port
	if _, _, err := (&InboundService{}).UpdateInbound(request); err != nil {
		t.Fatal(err)
	}
	clients := &ClientService{}
	if _, err := clients.Create(&InboundService{}, &ClientCreatePayload{Client: model.Client{Email: "acl-delete-survivor", Enable: true}}); err != nil {
		t.Fatal(err)
	}
	other, err := clients.GetRecordByEmail(nil, "acl-delete-survivor")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	siblingRequest := tunnelOwnerRequest(t, other.StableID, map[string]any{"enable": true, "settings": json.RawMessage(settings)})
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
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	flow, survivor := dial(tunnel.Port), dial(sibling.Port)
	managedActivationEcho(t, flow, "before")
	managedActivationEcho(t, survivor, "alive!")
	if err := db.Model(tunnel).Update("settings", settings).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Delete(&InboundService{}, owner.Id, false); err != nil {
		t.Fatal(err)
	}
	if currentXrayProcess() != process || !process.IsRunning() || process.TrafficDrainBootID() != boot {
		t.Fatal("owner deletion restarted shared core")
	}
	_ = flow.SetReadDeadline(time.Now().Add(2 * time.Second))
	var timeout net.Error
	if _, err := flow.Read(make([]byte, 1)); err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("deleted owner's TCP flow not closed: %v", err)
	}
	managedActivationEcho(t, survivor, "after!")
	for _, inbound := range process.GetConfig().InboundConfigs {
		if inbound.Tag == tunnel.Tag {
			t.Fatal("deleted owner's handler remains in running config")
		}
	}
	tcp, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port))
	if err != nil {
		t.Fatalf("deleted TCP listener still occupies port: %v", err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port))
	if err != nil {
		t.Fatalf("deleted UDP listener still occupies port: %v", err)
	}
	defer udp.Close()
}
