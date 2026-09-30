//go:build linux

package service

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestClientStandaloneCreationFeedsRealTunnelLedger(t *testing.T) {
	svc, seedInbound, seedOwner, targetPort := setupManagedActivationService(t)
	removeManagedTemplateOptIn(t, svc)
	inbounds, clients := &InboundService{}, &ClientService{}
	if _, err := inbounds.DelInbound(seedInbound.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Delete(inbounds, seedOwner.Id, false); err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{&model.ClientRecord{}, &model.Inbound{}} {
		var count int64
		if err := database.GetDB().Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("panel fixture is not empty: %T count=%d err=%v", model, count, err)
		}
	}
	if restart, err := clients.Create(inbounds, &ClientCreatePayload{Client: model.Client{
		Email: "first-tunnel-account", Enable: true, TotalGB: 1000,
		Policy: &model.ClientPolicyOptions{Multiplier: "1.5"},
	}}); err != nil || restart {
		t.Fatalf("first account: restart=%v err=%v", restart, err)
	}
	owner, err := clients.GetRecordByEmail(nil, "first-tunnel-account")
	if err != nil {
		t.Fatal(err)
	}
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
	request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"enable": true, "settings": json.RawMessage(settings)})
	request.Port = seedInbound.Port
	tunnel, _, err := inbounds.AddInbound(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{"tcp", "udp"} {
		flow, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = flow.Close() })
		managedActivationEcho(t, flow, "new"+network)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	total := policyLedgerTotal(t, owner.StableID)
	if total.RawUpload != 12 || total.RawDownload != 12 || total.BilledBytes != 36 {
		t.Fatalf("new owner TCP/UDP accounting: %+v", total)
	}
	reloaded, err := clients.GetRecordByEmail(nil, owner.Email)
	if err != nil || reloaded.StableID != owner.StableID || reloaded.UUID != "" || reloaded.Password != "" || reloaded.DesiredPolicyVersion == 0 {
		t.Fatalf("trusted listener ownership did not reach the running policy: %+v %v", reloaded, err)
	}
}
