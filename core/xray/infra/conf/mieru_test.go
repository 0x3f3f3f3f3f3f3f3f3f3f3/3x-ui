package conf_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestMieruNativeConfigRejectsUnusableIdentityAndTransport(t *testing.T) {
	for _, raw := range []string{
		`{"inbounds":[{"listen":"127.0.0.1","port":"38001-38002","protocol":"mieru","settings":{"users":[{"username":"alice","password":"secret","clientId":"alice-id","email":"alice"}]}}]}`,
		`{"inbounds":[{"listen":"127.0.0.1","port":38001,"protocol":"mieru","streamSettings":{"network":"ws"},"settings":{"users":[{"username":"alice","password":"secret","clientId":"alice-id","email":"alice"}]}}]}`,
		`{"outbounds":[{"protocol":"mieru","settings":{"address":"127.0.0.1","port":38001,"username":"alice","password":"secret","mtu":999999}}]}`,
		fmt.Sprintf(`{"inbounds":[{"listen":"127.0.0.1","port":38001,"protocol":"mieru","settings":{"users":[{"username":"alice","password":"secret","clientId":%q,"email":"alice"}]}}]}`, strings.Repeat("a", 129)),
	} {
		t.Run(raw, func(t *testing.T) {
			var config conf.Config
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Build(); err == nil {
				t.Fatal("unsafe native mieru configuration accepted")
			}
		})
	}
}

func TestMieruNativeConfigBuildsAuthenticatedInboundAndOutbound(t *testing.T) {
	const clientID = "11111111-1111-4111-8111-111111111111"
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			raw := fmt.Sprintf(`{"inbounds":[{"tag":"native-mieru","listen":"127.0.0.1","port":38443,"protocol":"mieru","settings":{"transport":%q,"users":[{"username":"alice","password":"business-secret","clientId":%q,"email":"canonical-alice"}]}}],"outbounds":[{"tag":"native-mieru-out","protocol":"mieru","settings":{"address":"127.0.0.1","port":38444,"transport":%q,"username":"alice","password":"business-secret","multiplexing":"MULTIPLEXING_OFF"}}]}`, mode, clientID, mode)
			var config conf.Config
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			built, err := config.Build()
			if err != nil {
				t.Fatalf("native mieru protocol cannot construct a real core configuration: %v", err)
			}
			inbound, err := built.Inbound[0].ProxySettings.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := protojson.Marshal(inbound)
			if err != nil || !strings.Contains(string(encoded), clientID) || !strings.Contains(string(encoded), "canonical-alice") || !strings.Contains(string(inbound.ProtoReflect().Descriptor().FullName()), ".mieru.") {
				t.Fatalf("native inbound lost trusted identity or native type: %s/%v", encoded, err)
			}
			outbound, err := built.Outbound[0].ProxySettings.GetInstance()
			if err != nil || !strings.Contains(string(outbound.ProtoReflect().Descriptor().FullName()), ".mieru.") {
				t.Fatalf("outbound is not a native mieru protocol: %v", err)
			}
		})
	}
}
