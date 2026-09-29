package job

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	clientapi "github.com/enfein/mieru/v3/apis/client"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestMieruCollectorsObserveIdleClientsWithoutCoreOnlineAPI(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) { testMieruIdleCollectors(t, underlay) })
	}
}

func testMieruIdleCollectors(t *testing.T, underlay string) {
	t.Helper()
	binary := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for actual mieru online/IP collection")
	}
	setupIntegrationDB(t)
	t.Setenv("XUI_ENABLE_FAIL2BAN", "true")
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	if err := os.Symlink(binary, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	freePort := func() int {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().(*net.TCPAddr).Port
	}
	template := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"api":       map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"stats":     map[string]any{},
		"inbounds":  []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": freePort(), "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}},
		"outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}}},
	}
	encoded, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	if err := db.Where("key = ?", "xrayTemplateConfig").Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Setting{Key: "xrayTemplateConfig", Value: string(encoded)}).Error; err != nil {
		t.Fatal(err)
	}
	user := model.Client{Email: "mieru-idle-collector", Password: "native-idle-password", Enable: true}
	settings, err := json.Marshal(map[string]any{"network": underlay, "clients": []model.Client{user}})
	if err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{Protocol: model.Mieru, Enable: true, Listen: "127.0.0.1", Port: freePort(), Settings: string(settings)}
	inboundSvc, svc := &service.InboundService{}, &service.XrayService{}
	if _, _, err := inboundSvc.AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.StopXray() })
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		statuses, err := inboundSvc.GetMieruRuntimeStatuses(0)
		if err != nil {
			t.Fatal(err)
		}
		if len(statuses) == 1 && statuses[0].State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mieru runtime not ready: %+v", statuses)
		}
		time.Sleep(10 * time.Millisecond)
	}
	transport := appctlpb.TransportProtocol_TCP
	if underlay == "udp" {
		transport = appctlpb.TransportProtocol_UDP
	}
	client := clientapi.NewClient()
	if err := client.Store(&clientapi.ClientConfig{Profile: &appctlpb.ClientProfile{ProfileName: proto.String("idle-presence"), User: &appctlpb.User{Name: proto.String(user.Email), Password: proto.String(user.Password)}, Servers: []*appctlpb.ServerEndpoint{{IpAddress: proto.String("127.0.0.1"), PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(inbound.Port)), Protocol: transport.Enum()}}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })
	connection, err := client.DialContext(t.Context(), apimodel.NetAddrSpec{Net: "udp", AddrSpec: apimodel.AddrSpec{FQDN: "route.invalid", Port: 443}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	process := service.XrayProcess()
	process.SetOnlineAPISupport(xray.OnlineAPIUnsupported)
	NewXrayTrafficJob().Run()
	if got := inboundSvc.GetOnlineClients(); !slices.Equal(got, []string{user.Email}) {
		t.Fatalf("idle mieru client missing from existing online view: %v", got)
	}
	if got := process.GetLocalActiveInbounds(); !slices.Contains(got, inbound.Tag) {
		t.Fatalf("idle mieru inbound missing from active attribution: %v", got)
	}
	var traffic xray.ClientTraffic
	if err := db.Where("email = ?", user.Email).First(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	if traffic.LastOnline < time.Now().UnixMilli()-2000 || traffic.Up != 0 || traffic.Down != 0 {
		t.Fatalf("idle native observation invented payload or lost last-online: %+v", traffic)
	}
	ipJob := NewCheckClientIpJob()
	observed, ok := ipJob.collectFromOnlineAPI()
	if !ok || len(observed) != 1 || len(observed[user.Email]) != 1 || observed[user.Email]["127.0.0.1"] < time.Now().Unix()-2 {
		t.Fatalf("idle native peer missing without core online RPC: %+v available=%v", observed, ok)
	}
	ipJob.Run()
	ips, err := inboundSvc.GetClientIpsWithNodes(user.Email)
	if err != nil || len(ips) != 1 || ips[0].IP != "127.0.0.1" || ips[0].Node != "" {
		t.Fatalf("existing IP view lost native peer: %+v err=%v", ips, err)
	}
	_ = connection.Close()
	deadline = time.Now().Add(2 * time.Second)
	for {
		observed, _ = ipJob.collectFromOnlineAPI()
		if len(observed) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed native association remained online: %+v", observed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	process.RefreshLocalOnline(nil, nil, time.Now().Add(21*time.Second).UnixMilli(), 20000)
	if got := inboundSvc.GetOnlineClients(); len(got) != 0 {
		t.Fatalf("native online entry survived the existing grace window: %v", got)
	}
}
