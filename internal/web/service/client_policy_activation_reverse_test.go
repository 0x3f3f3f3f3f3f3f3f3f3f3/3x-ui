package service

import (
	"context"
	"net"
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/proxy/vless"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyEditingClearsRunningVLESSReverse(t *testing.T) {
	svc, _, _, _ := setupManagedActivationService(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	inbound := mkInbound(t, port, model.VLESS, `{"decryption":"none","clients":[]}`)
	client := reverseProbeClient("managed-reverse", true)
	client.SubID = "reverse-sub"
	cs, is := &ClientService{}, &InboundService{}
	if _, err := cs.Create(is, &ClientCreatePayload{Client: client, InboundIds: []int{inbound.Id}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	var api xray.XrayAPI
	if err := api.InitEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	check := func(want string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		response, err := (*api.HandlerServiceClient).GetInboundUsers(ctx, &command.GetInboundUserRequest{Tag: inbound.Tag, Email: client.Email})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Users) != 1 {
			t.Fatalf("expected one running account: %+v", response)
		}
		account, err := response.Users[0].Account.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		if got := account.(*vless.Account).GetReverse().GetTag(); got != want {
			t.Fatalf("running reverse tag = %q, want %q", got, want)
		}
	}
	check("portal")
	record, err := cs.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	client.Reverse = nil
	if _, err := cs.Update(is, record.Id, client, 0); err != nil {
		t.Fatal(err)
	}
	stored, err := cs.GetRecordByEmail(nil, client.Email)
	if err != nil || stored.Reverse != "" {
		t.Fatalf("reverse clear did not commit: %+v %v", stored, err)
	}
	if !currentXrayProcess().IsRunning() {
		t.Fatal("reverse edit left the core unavailable")
	}
	api.Close()
	endpoint, err = currentXrayProcess().GetAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if err := api.InitEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	check("")
}

func TestClientPolicyEditingOtherProtocolPreservesSharedReverse(t *testing.T) {
	_, email, _ := seedReverseProbeInbound(t, "shared-reverse", 50075, true)
	cs, is := &ClientService{}, &InboundService{}
	record := lookupClientRecord(t, email)
	inbound := mkInbound(t, 50076, model.Trojan, `{"clients":[]}`)
	client := record.ToClient()
	client.Password = "shared-trojan-secret"
	client.Reverse = nil
	if err := database.GetDB().Model(inbound).Update("settings", clientsSettings(t, []model.Client{*client})).Error; err != nil {
		t.Fatal(err)
	}
	if err := cs.SyncInbound(nil, inbound.Id, []model.Client{*client}); err != nil {
		t.Fatal(err)
	}
	client.Comment = "ordinary Trojan edit"
	if _, err := cs.UpdateInboundClient(is, &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{*client})}, email); err != nil {
		t.Fatal(err)
	}
	stored := lookupClientRecord(t, email)
	if stored.ToClient().Reverse == nil || stored.ToClient().Reverse.Tag != "portal" {
		t.Fatalf("non-VLESS edit cleared shared reverse: %q", stored.Reverse)
	}
}
