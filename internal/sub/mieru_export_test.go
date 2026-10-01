package sub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enfein/mieru/v3/pkg/appctl"
	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
)

func TestMieruShareLinkOfficialParserPreservesNativeProfile(t *testing.T) {
	for _, transport := range []string{"TCP", "UDP"} {
		for _, address := range []string{"native.example.test", "2001:db8::1"} {
			t.Run(transport+"/"+address, func(t *testing.T) {
				client := model.Client{Email: "label", MieruUsername: "用户:@%", MieruPassword: "密码:/?%", Password: "other-protocol", Enable: true}
				settings, _ := json.Marshal(map[string]any{"transport": transport, "mtu": 1400, "clients": []model.Client{client}})
				inbound := &model.Inbound{Id: 91, Protocol: model.Mieru, Listen: address, Port: 8443, Remark: "native profile", Settings: string(settings)}
				svc := &SubService{}
				svc.primeLinkClients(inbound.Id, []model.Client{client}, true)
				link := svc.GetLink(inbound, client.Email)
				profile, err := appctl.URLToClientProfile(link)
				if err != nil {
					t.Fatalf("public share link rejected by official parser: %q: %v", link, err)
				}
				if profile.GetUser().GetName() != client.MieruUsername || profile.GetUser().GetPassword() != client.MieruPassword {
					t.Fatalf("native credentials changed: %v", profile.GetUser())
				}
				server := profile.GetServers()[0]
				actualAddress := server.GetDomainName()
				if actualAddress == "" {
					actualAddress = server.GetIpAddress()
				}
				if actualAddress != address || server.GetPortBindings()[0].GetPort() != 8443 || server.GetPortBindings()[0].GetProtocol().String() != transport || profile.GetMtu() != 1400 {
					t.Fatalf("profile changed: %v", profile)
				}
				if profile.GetMultiplexing().GetLevel().String() != "MULTIPLEXING_LOW" || profile.GetProfileName() == "" {
					t.Fatalf("profile defaults lost: %v", profile)
				}
				if strings.Contains(link, client.Password) {
					t.Fatal("export consumed other protocol password")
				}
			})
		}
	}
}

func TestMieruOfficialSubscriptionConfigAndUnsupportedFormats(t *testing.T) {
	cleanup, err := testpg.IsolatePackage("mieru_subscription")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	oldFS := distFS
	distFS = testDistFS
	t.Cleanup(func() { distFS = oldFS })
	seedSubDB(t)
	db := database.GetDB()
	client := model.ClientRecord{Email: "label", SubID: "native-sub", MieruUsername: "独立-user", MieruPassword: "独立-password", Password: "other-protocol", Enable: true}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{UserId: 1, Protocol: model.Mieru, Enable: true, Listen: "2001:db8::1", Port: 8443, Tag: "native", Remark: "native profile", Settings: `{"transport":"UDP","mtu":1400,"clients":[]}`, StreamSettings: "{}"}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	base := NewSubService("")
	links, _, _, _, err := base.GetSubs("native-sub", "native.example.test")
	if err != nil || len(links) != 1 {
		t.Fatalf("raw native subscription: %v %v", links, err)
	}
	profile, err := appctl.URLToClientProfile(links[0])
	if err != nil || profile.GetUser().GetName() != client.MieruUsername {
		t.Fatalf("official raw URL: %v %v", profile, err)
	}
	router := newSubscriptionTestRouter(subscriptionTestRouterConfig{})
	request := httptest.NewRequest(http.MethodGet, "/sub/native-sub?format=mieru", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("official config response: %d %s %s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	config := &pb.ClientConfig{}
	if err := protojson.Unmarshal(response.Body.Bytes(), config); err != nil {
		t.Fatalf("official JSON parser: %v", err)
	}
	if len(config.GetProfiles()) != 1 || config.GetActiveProfile() != config.GetProfiles()[0].GetProfileName() || config.GetProfiles()[0].GetUser().GetPassword() != client.MieruPassword || config.GetProfiles()[0].GetServers()[0].GetPortBindings()[0].GetProtocol() != pb.TransportProtocol_UDP {
		t.Fatalf("official config changed: %v", config)
	}
	url, err := appctl.ClientConfigToURL(config)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := appctl.URLToClientConfig(url)
	if err != nil || decoded.GetProfiles()[0].GetUser().GetName() != client.MieruUsername {
		t.Fatalf("official config URL round trip: %v %v", decoded, err)
	}
	browser := httptest.NewRequest(http.MethodGet, "/sub/native-sub?format=mieru", nil)
	browser.Header.Set("Accept", "text/html")
	download := httptest.NewRecorder()
	router.ServeHTTP(download, browser)
	if download.Code != 200 || !strings.Contains(download.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("explicit mieru download became browser HTML: %d %s", download.Code, download.Header().Get("Content-Type"))
	}
	if _, _, err := NewSubJsonService("", "", "", "", base).GetJson("native-sub", "native.example.test", false); err == nil || !strings.Contains(strings.ToLower(err.Error()), "mieru") {
		t.Fatalf("unsupported Xray JSON format did not explain native mieru: %v", err)
	}
	if _, _, err := NewSubClashService(false, "", base).GetClash("native-sub", "native.example.test"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "mieru") {
		t.Fatalf("unsupported configured Clash format did not explain native mieru: %v", err)
	}
}

func TestMieruShareLinkHostEndpointsPreserveNativeUDP(t *testing.T) {
	client := model.Client{Email: "label", MieruUsername: "user", MieruPassword: "secret", Enable: true}
	settings, _ := json.Marshal(map[string]any{"transport": "UDP", "mtu": 1400, "clients": []model.Client{client}})
	inbound := &model.Inbound{Id: 92, Protocol: model.Mieru, Listen: "127.0.0.1", Port: 8443, Remark: "native", Settings: string(settings), StreamSettings: "{}"}
	svc := &SubService{}
	svc.primeLinkClients(inbound.Id, []model.Client{client}, true)
	link := svc.linkFromHosts(inbound, client, []map[string]any{{"dest": "[2001:db8::2]", "port": float64(9443), "remark": "public"}})
	profile, err := appctl.URLToClientProfile(link)
	if err != nil {
		t.Fatalf("official host URL: %q %v", link, err)
	}
	server := profile.GetServers()[0]
	if server.GetIpAddress() != "2001:db8::2" || server.GetPortBindings()[0].GetPort() != 9443 || server.GetPortBindings()[0].GetProtocol().String() != "UDP" {
		t.Fatalf("host profile changed: %v", profile)
	}
}

func TestMieruExternalShareFormatsAreExplicit(t *testing.T) {
	cleanup, err := testpg.IsolatePackage("mieru_external_export")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	seedSubDB(t)
	client := model.ClientRecord{Email: "external-native", SubID: "external-native-sub", Enable: true}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	link := "mierus://user:secret@native.example.test?profile=external&port=8443&protocol=UDP"
	if err := database.GetDB().Create(&model.ClientExternalLink{ClientId: client.Id, Kind: model.ExternalLinkKindLink, Value: link}).Error; err != nil {
		t.Fatal(err)
	}
	base := NewSubService("")
	config, _, err := base.GetMieruConfig(client.SubID, "panel.test")
	if err != nil || config == "" {
		t.Fatalf("external official mieru config lost: %v", err)
	}
	if _, _, err := NewSubJsonService("", "", "", "", base).GetJson(client.SubID, "panel.test", false); err == nil || !strings.Contains(err.Error(), "mieru") {
		t.Fatalf("external native profile was emitted as generic Xray JSON: %v", err)
	}
	if _, _, err := NewSubClashService(false, "", base).GetClash(client.SubID, "panel.test"); err == nil || !strings.Contains(err.Error(), "mieru") {
		t.Fatalf("external native profile was silently omitted by Clash: %v", err)
	}
}
