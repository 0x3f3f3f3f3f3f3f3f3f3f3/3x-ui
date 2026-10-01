package link

import (
	"testing"

	"github.com/enfein/mieru/v3/pkg/appctl"
	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestMieruOfficialLinksAndClientConfigReimport(t *testing.T) {
	profile := &pb.ClientProfile{ProfileName: proto.String("native label"), User: &pb.User{Name: proto.String("用户:@%"), Password: proto.String("密码:/?%")}, Servers: []*pb.ServerEndpoint{{IpAddress: proto.String("2001:db8::1"), PortBindings: []*pb.PortBinding{{Port: proto.Int32(8443), Protocol: pb.TransportProtocol_UDP.Enum()}}}}, Mtu: proto.Int32(1400), Multiplexing: &pb.MultiplexingConfig{Level: pb.MultiplexingLevel_MULTIPLEXING_HIGH.Enum()}}
	urls, err := appctl.ClientProfileToMultiURLs(profile)
	if err != nil {
		t.Fatal(err)
	}
	config := &pb.ClientConfig{Profiles: []*pb.ClientProfile{profile}, ActiveProfile: profile.ProfileName, Socks5Port: proto.Int32(1080)}
	configURL, err := appctl.ClientConfigToURL(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range append(urls, configURL) {
		parsed, err := ParseLink(raw)
		if err != nil || parsed == nil {
			t.Fatalf("official link not reimported: %v", err)
		}
		settings := parsed.Outbound["settings"].(map[string]any)
		if parsed.Outbound["protocol"] != "mieru" || settings["address"] != "2001:db8::1" || settings["username"] != profile.GetUser().GetName() || settings["password"] != profile.GetUser().GetPassword() || settings["transport"] != "UDP" || settings["multiplexing"] != "MULTIPLEXING_HIGH" {
			t.Fatalf("native settings lost: %#v", parsed.Outbound)
		}
	}
	jsonConfig, err := protojson.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, ids, err := ParseSubscriptionBody(jsonConfig)
	if err != nil || len(parsed) != 1 || len(ids) != 1 || parsed[0]["protocol"] != "mieru" {
		t.Fatalf("official JSON not reimported: %#v %v", parsed, err)
	}
	unsupported := proto.Clone(profile).(*pb.ClientProfile)
	unsupported.HandshakeMode = pb.HandshakeMode_HANDSHAKE_NO_WAIT.Enum()
	badURLs, err := appctl.ClientProfileToMultiURLs(unsupported)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLink(badURLs[0]); err == nil {
		t.Fatal("unrepresentable handshake mode was silently dropped")
	}
	if _, _, err := ParseSubscriptionBody([]byte(badURLs[0])); err == nil {
		t.Fatal("subscription silently dropped unrepresentable native profile")
	}
}

func TestMieruImportRejectsSilentProfileLoss(t *testing.T) {
	base := "mierus://user:secret@native.test?profile=p&port=8443&protocol=TCP"
	if _, err := ParseLink(base); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{base + "&multiplexing=unknown", base + "&mtu=1400&mtu=1500", base + "&unknown-option=required", "mierus://user:secret@native.test:9999?profile=p&port=8443&protocol=TCP"} {
		if _, err := ParseLink(raw); err == nil {
			t.Errorf("silently dropped native option: %s", raw)
		}
	}
}
