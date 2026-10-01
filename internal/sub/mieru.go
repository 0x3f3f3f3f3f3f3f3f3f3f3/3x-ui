package sub

import (
	"errors"
	"fmt"
	"strings"

	"github.com/enfein/mieru/v3/pkg/appctl"
	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

var errMieruClientFormat = errors.New("mieru requires the official client configuration: use the subscription URL with format=mieru")

func isMieruShareLink(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "mierus://") || strings.HasPrefix(raw, "mieru://")
}

// GetMieruConfig exports the official client format from the same eligible
// subscription links used by QR and raw subscriptions.
func (s *SubService) GetMieruConfig(subID, host string) (string, string, error) {
	request := s.ForRequest(host)
	request.subscriptionBody = true
	links, _, _, traffic, err := request.getSubs(subID)
	if err != nil || links == nil {
		return "", "", err
	}
	config := &pb.ClientConfig{Socks5Port: proto.Int32(1080)}
	names := make(map[string]bool)
	for _, link := range links {
		for _, raw := range strings.Split(link, "\n") {
			if !strings.HasPrefix(raw, "mierus://") {
				continue
			}
			profile, err := appctl.URLToClientProfile(raw)
			if err != nil {
				return "", "", fmt.Errorf("invalid mieru subscription profile: %w", err)
			}
			base := profile.GetProfileName()
			name := base
			for n := 2; names[name]; n++ {
				name = fmt.Sprintf("%s (%d)", base, n)
			}
			profile.ProfileName = proto.String(name)
			names[name] = true
			config.Profiles = append(config.Profiles, profile)
		}
	}
	if len(config.Profiles) == 0 {
		return "", "", nil
	}
	config.ActiveProfile = proto.String(config.Profiles[0].GetProfileName())
	if err := appctl.ValidateFullClientConfig(config); err != nil {
		return "", "", err
	}
	body, err := (protojson.MarshalOptions{Indent: "  "}).Marshal(config)
	return string(body), request.subscriptionUserinfo(traffic), err
}

func (s *SubService) genMieruLink(inbound *model.Inbound, email string) string {
	client, ok := s.clientForLink(inbound, email)
	if !ok {
		return ""
	}
	stream := unmarshalStreamSettings(inbound.StreamSettings)
	endpoints, _ := stream["externalProxy"].([]any)
	if len(endpoints) == 0 {
		endpoints = []any{map[string]any{"dest": s.resolveInboundAddress(inbound), "port": float64(inbound.Port)}}
	}
	var links []string
	for _, value := range endpoints {
		endpoint, ok := value.(map[string]any)
		if !ok {
			continue
		}
		address, _ := endpoint["dest"].(string)
		port, _ := endpoint["port"].(float64)
		profile, err := service.MieruClientProfile(inbound, client, address, int(port), s.endpointRemark(inbound, email, endpoint, ""))
		if err != nil {
			continue
		}
		urls, err := appctl.ClientProfileToMultiURLs(profile)
		if err == nil {
			links = append(links, urls...)
		}
	}
	return strings.Join(links, "\n")
}
