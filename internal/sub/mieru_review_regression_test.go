package sub

import (
	"testing"

	"github.com/enfein/mieru/v3/pkg/appctl"
	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/link"
)

func TestMieruExternalRemarkRoundTrip(t *testing.T) {
	cleanup, err := testpg.IsolatePackage("mieru_external_remark")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	seedSubDB(t)
	client := model.ClientRecord{Email: "external", SubID: "native-remark", Enable: true}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	raw := "mierus://user:secret@native.example.test?profile=original&port=8443&protocol=UDP"
	if err := database.GetDB().Create(&model.ClientExternalLink{ClientId: client.Id, Kind: model.ExternalLinkKindLink, Value: raw, Remark: "Renamed profile"}).Error; err != nil {
		t.Fatal(err)
	}
	links, _, _, _, err := NewSubService("").GetSubs(client.SubID, "panel.test")
	if err != nil || len(links) != 1 {
		t.Fatalf("native subscription unavailable: %v", err)
	}
	if _, _, err := link.ParseSubscriptionBody([]byte(links[0])); err != nil {
		t.Fatalf("public named native export cannot reimport: %s: %v", links[0], err)
	}
	profile, err := appctl.URLToClientProfile(links[0])
	if err != nil {
		t.Fatal(err)
	}
	if profile.GetProfileName() != "Renamed profile" {
		t.Fatal("native remark was not projected into official profile")
	}
}

func TestMieruExternalFullConfigDownload(t *testing.T) {
	cleanup, err := testpg.IsolatePackage("mieru_external_full")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	seedSubDB(t)
	profile, err := appctl.URLToClientProfile("mierus://user:secret@native.example.test?profile=full&port=8443&protocol=TCP")
	if err != nil {
		t.Fatal(err)
	}
	port := int32(1080)
	raw, err := appctl.ClientConfigToURL(&pb.ClientConfig{Profiles: []*pb.ClientProfile{profile}, ActiveProfile: profile.ProfileName, Socks5Port: &port})
	if err != nil {
		t.Fatal(err)
	}
	client := model.ClientRecord{Email: "full-external", SubID: "native-full", Enable: true}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientExternalLink{ClientId: client.Id, Kind: model.ExternalLinkKindLink, Value: raw, Remark: "完整 named profile"}).Error; err != nil {
		t.Fatal(err)
	}
	body, _, err := NewSubService("").GetMieruConfig(client.SubID, "panel.test")
	if err != nil || body == "" {
		t.Fatalf("official full-config external link silently omitted: bodyempty=%t err=%v", body == "", err)
	}
	decoded := &pb.ClientConfig{}
	if err := protojson.Unmarshal([]byte(body), decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Profiles) != 1 {
		t.Fatal("full-config profile lost")
	}
	result := decoded.Profiles[0]
	if result.GetProfileName() != "完整 named profile" || result.GetUser().GetName() != "user" || result.GetUser().GetPassword() != "secret" || result.GetServers()[0].GetPortBindings()[0].GetProtocol() != pb.TransportProtocol_TCP {
		t.Fatal("full-config profile name, authentication or transport changed")
	}
}
