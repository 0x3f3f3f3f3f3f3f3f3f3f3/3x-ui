package sub

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/enfein/mieru/v3/pkg/appctl"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestMieruShareLinkUsesOfficialNativeProfile(t *testing.T) {
	for _, test := range []struct {
		network string
		host    string
		want    []appctlpb.TransportProtocol
	}{
		{"tcp", "203.0.113.17", []appctlpb.TransportProtocol{appctlpb.TransportProtocol_TCP}},
		{"udp", "[2001:db8::17]", []appctlpb.TransportProtocol{appctlpb.TransportProtocol_UDP}},
		{"both", "native.example", []appctlpb.TransportProtocol{appctlpb.TransportProtocol_TCP, appctlpb.TransportProtocol_UDP}},
	} {
		t.Run(test.network, func(t *testing.T) {
			settings, err := json.Marshal(map[string]any{"network": test.network, "bridgePort": 47221, "clients": []model.Client{{Email: "native-user", Password: "fixture:p@ss/#?中文", Enable: true, TotalGB: 12345}}})
			if err != nil {
				t.Fatal(err)
			}
			inbound := &model.Inbound{Protocol: model.Mieru, Port: 8443, Listen: "0.0.0.0", ShareAddrStrategy: "custom", ShareAddr: test.host, Remark: "native profile", Settings: string(settings)}
			link := (&SubService{}).GetLink(inbound, "native-user")
			profile, err := appctl.URLToClientProfile(link)
			if err != nil {
				t.Fatalf("share output is not an official mierus profile: %v", err)
			}
			if err := appctlcommon.ValidateClientConfigSingleProfile(profile); err != nil {
				t.Fatalf("official profile validation: %v", err)
			}
			if profile.GetUser().GetName() != "native-user" || profile.GetUser().GetPassword() != "fixture:p@ss/#?中文" || len(profile.GetUser().GetQuotas()) != 0 {
				t.Fatal("native export lost credentials or injected server-side rolling quotas")
			}
			if len(profile.GetServers()) != 1 {
				t.Fatal("expected one native advertised endpoint")
			}
			server := profile.GetServers()[0]
			host := server.GetIpAddress()
			if host == "" {
				host = server.GetDomainName()
			}
			if host != strings.Trim(test.host, "[]") || len(server.GetPortBindings()) != len(test.want) {
				t.Fatalf("native endpoint changed: host=%s bindings=%d", host, len(server.GetPortBindings()))
			}
			for i, binding := range server.GetPortBindings() {
				if binding.GetPort() != 8443 || binding.GetProtocol() != test.want[i] {
					t.Fatal("export advertised an internal bridge or the wrong native transport")
				}
			}
		})
	}
}

func TestMieruShareLinkAppliesHostOverridesWithoutTLSFields(t *testing.T) {
	inbound := &model.Inbound{Protocol: model.Mieru, Port: 8443, Listen: "203.0.113.17", Settings: `{"network":"udp","bridgePort":47221,"clients":[{"email":"native-user","password":"fixture-password","enable":true}]}`}
	client := model.Client{Email: "native-user", Password: "fixture-password", Enable: true}
	svc := &SubService{}
	link := svc.linkFromHosts(inbound, client, []map[string]any{{"dest": "edge.example", "port": float64(9443), "remark": "native edge", "isHost": true, "forceTls": "tls", "sni": "irrelevant.example"}})
	profile, err := appctl.URLToClientProfile(link)
	if err != nil {
		t.Fatalf("host export is not an official native profile: %v", err)
	}
	server := profile.GetServers()[0]
	if server.GetDomainName() != "edge.example" || server.GetPortBindings()[0].GetPort() != 9443 || server.GetPortBindings()[0].GetProtocol() != appctlpb.TransportProtocol_UDP {
		t.Fatal("native host address/port override or UDP underlay was lost")
	}
	if strings.Contains(link, "irrelevant.example") || strings.Contains(link, "47221") {
		t.Fatal("export included unused TLS metadata or a private routing port")
	}
}

func TestManagedNativeProtocolsNeverExportDirectOnlyXrayProfiles(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mieru, model.SSH, model.MTProto} {
		t.Run(string(protocol), func(t *testing.T) {
			inbound := &model.Inbound{Protocol: protocol, Port: 8443, Listen: "203.0.113.17", Settings: `{}`}
			sub := NewSubService("")
			jsonService := NewSubJsonService("", "", "", "", sub)
			configs := jsonService.getConfig(sub, inbound, model.Client{Email: "native-user", Password: "fixture-password"}, "panel.example")
			if len(configs) != 0 {
				t.Fatalf("unsupported native protocol produced %d Xray profiles that bypass its proxy", len(configs))
			}
		})
	}
}

func TestMieruMihomoExportPreservesNativeTransportsAndHosts(t *testing.T) {
	for _, test := range []struct {
		network    string
		transports []string
	}{
		{"tcp", []string{"TCP"}}, {"udp", []string{"UDP"}}, {"both", []string{"TCP", "UDP"}},
	} {
		t.Run(test.network, func(t *testing.T) {
			inbound := &model.Inbound{Protocol: model.Mieru, Listen: "wrong.example", Port: 8443, Settings: `{"network":"` + test.network + `","bridgePort":47221}`, StreamSettings: `{"externalProxy":[{"dest":"[2001:db8::17]","port":9443,"remark":"native edge","remarkFinal":true,"forceTls":"tls","sni":"irrelevant.example"}]}`}
			client := model.Client{Email: "native-user", Password: "fixture-password", TotalGB: 12345}
			proxies := (&SubClashService{}).getProxies(NewSubService(""), inbound, client, "panel.example")
			if len(proxies) != len(test.transports) {
				t.Fatalf("native Mihomo proxies=%d, want %d", len(proxies), len(test.transports))
			}
			for i, transport := range test.transports {
				name := "native edge"
				if test.network == "both" {
					name += " (" + transport + ")"
				}
				want := map[string]any{"name": name, "type": "mieru", "server": "2001:db8::17", "port": 9443, "transport": transport, "username": "native-user", "password": "fixture-password", "udp": true}
				if !reflect.DeepEqual(proxies[i], want) {
					t.Fatal("Mihomo export lost native endpoint/credentials or included unrelated Xray/rolling-quota fields")
				}
			}
			if legacy := legacyClashProxies(proxies); len(legacy) != 0 {
				t.Fatalf("legacy Clash received %d unsupported mieru nodes", len(legacy))
			}
		})
	}
}
