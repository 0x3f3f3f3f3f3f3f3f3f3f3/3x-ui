package service

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	miClient "github.com/enfein/mieru/v3/apis/client"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func officialMieruPanelClient(t *testing.T, port int, transport, username, password string) miClient.Client {
	t.Helper()
	mode := appctlpb.TransportProtocol_TCP
	if transport == "UDP" {
		mode = appctlpb.TransportProtocol_UDP
	}
	client := miClient.NewClient()
	if err := client.Store(&miClient.ClientConfig{Profile: &appctlpb.ClientProfile{
		ProfileName: proto.String("panel-runtime"), Multiplexing: &appctlpb.MultiplexingConfig{Level: appctlpb.MultiplexingLevel_MULTIPLEXING_HIGH.Enum()},
		User:    &appctlpb.User{Name: proto.String(username), Password: proto.String(password)},
		Servers: []*appctlpb.ServerEndpoint{{IpAddress: proto.String("127.0.0.1"), PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: mode.Enum()}}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })
	return client
}

func TestMieruPublicCRUDRealCorePreservesSiblingAndSharedLedger(t *testing.T) {
	for _, transport := range []string{"TCP", "UDP"} {
		t.Run(transport, func(t *testing.T) {
			svc, tunnel, owner, targetPort := setupManagedActivationService(t)
			removeManagedTemplateOptIn(t, svc)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			boot := process.TrafficDrainBootID()
			probe, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			inbounds, clients := &InboundService{}, &ClientService{}
			listener, _, err := inbounds.AddInbound(&model.Inbound{Tag: "mieru-live", Enable: true, Protocol: model.Mieru, Listen: "127.0.0.1", Port: port, Settings: fmt.Sprintf(`{"transport":%q,"clients":[]}`, transport)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := clients.Attach(inbounds, owner.Id, []int{listener.Id}); err != nil {
				t.Fatal(err)
			}
			stored, err := clients.GetRecordByEmail(nil, owner.Email)
			if err != nil {
				t.Fatal(err)
			}
			if stored.MieruUsername == "" || stored.MieruPassword == "" {
				t.Fatal("native attach did not create independent credentials")
			}
			sibling := model.Client{Email: "mieru-sibling", Enable: true, MieruUsername: "sibling-wire", MieruPassword: "sibling-secret"}
			if _, err := clients.Create(inbounds, &ClientCreatePayload{Client: sibling, InboundIds: []int{listener.Id}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			target := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: targetPort}
			dial := func(client miClient.Client) net.Conn {
				t.Helper()
				c, err := client.DialContext(ctx, target)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				return c
			}
			oldClient := officialMieruPanelClient(t, port, transport, stored.MieruUsername, stored.MieruPassword)
			first := dial(oldClient)
			other := dial(officialMieruPanelClient(t, port, transport, sibling.MieruUsername, sibling.MieruPassword))
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
			// The 300 historical legacy bytes retain their prior billed value;
			// only newly decoded payload uses this owner's multiplier of two.
			if before.RawUpload != 111 || before.RawDownload != 211 || before.BilledBytes != 344 {
				t.Fatalf("native/Tunnel payload did not share existing owner ledger: %+v", before)
			}
			update := *stored.ToClient()
			update.MieruPassword = "rotated-native-secret"
			if _, err := clients.Update(inbounds, stored.Id, update, 0); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, first)
			probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
			stop := context.AfterFunc(probeCtx, func() { _ = oldClient.Stop() })
			stale, staleErr := oldClient.DialContext(probeCtx, target)
			stop()
			probeCancel()
			if staleErr == nil {
				stale.Close()
				t.Fatal("revoked cached native credential reauthenticated")
			}
			rotated := dial(officialMieruPanelClient(t, port, transport, stored.MieruUsername, update.MieruPassword))
			managedActivationEcho(t, rotated, "fresh")
			managedActivationEcho(t, other, "alive")
			managedActivationEcho(t, shared, "stay")
			if process != currentXrayProcess() || boot != currentXrayProcess().TrafficDrainBootID() {
				t.Fatal("native account rotation restarted sibling listeners")
			}
			if _, _, err := clients.SetClientEnableByEmail(inbounds, owner.Email, false); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, rotated)
			managedActivationClosed(t, shared)
			managedActivationEcho(t, other, "kept")
			if _, _, err := clients.SetClientEnableByEmail(inbounds, owner.Email, true); err != nil {
				t.Fatal(err)
			}
			current := dial(officialMieruPanelClient(t, port, transport, stored.MieruUsername, update.MieruPassword))
			managedActivationEcho(t, current, "again")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 125 || total.RawDownload != 225 || total.BilledBytes != 400 {
				t.Fatalf("native credential/enable lifecycle reset or repriced history: %+v", total)
			}
			if _, err := clients.Delete(inbounds, owner.Id, true); err != nil {
				t.Fatal(err)
			}
			managedActivationClosed(t, current)
			managedActivationEcho(t, other, "after-delete")
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			reopened := dial(officialMieruPanelClient(t, port, transport, sibling.MieruUsername, sibling.MieruPassword))
			managedActivationEcho(t, reopened, "restart")
			var count int64
			if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", owner.StableID).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("native client delete retained canonical authentication")
			}
		})
	}
}
