package sub

import (
	"net"
	"strings"

	"github.com/enfein/mieru/v3/pkg/appctl"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func (s *SubService) genMieruLink(inbound *model.Inbound, email string) string {
	client, ok := s.clientForLink(inbound, email)
	if !ok {
		return ""
	}
	var links []string
	for _, profile := range s.mieruProfiles(inbound, client) {
		urls, err := appctl.ClientProfileToMultiURLs(profile)
		if err == nil {
			links = append(links, urls...)
		}
	}
	return strings.Join(links, "\n")
}

func (s *SubService) mieruProfiles(inbound *model.Inbound, client model.Client) []*appctlpb.ClientProfile {
	network, _ := s.linkSettings(inbound)["network"].(string)
	var transports []appctlpb.TransportProtocol
	switch network {
	case "", "tcp":
		transports = []appctlpb.TransportProtocol{appctlpb.TransportProtocol_TCP}
	case "udp":
		transports = []appctlpb.TransportProtocol{appctlpb.TransportProtocol_UDP}
	case "both":
		transports = []appctlpb.TransportProtocol{appctlpb.TransportProtocol_TCP, appctlpb.TransportProtocol_UDP}
	default:
		return nil
	}
	endpoints := []ShareEndpoint{s.inboundDefaultEndpoint(inbound)}
	stream := unmarshalStreamSettings(inbound.StreamSettings)
	if proxies, ok := stream["externalProxy"].([]any); ok && len(proxies) > 0 {
		endpoints = nil
		for _, raw := range proxies {
			if ep, ok := raw.(map[string]any); ok {
				endpoints = append(endpoints, externalProxyToEndpoint(ep))
			}
		}
	}
	var profiles []*appctlpb.ClientProfile
	for _, endpoint := range endpoints {
		if endpoint.Port < 1 || endpoint.Port > 65535 {
			continue
		}
		name := s.endpointRemark(inbound, client.Email, endpoint.ep, network)
		if name == "" {
			name = "mieru"
		}
		server := &appctlpb.ServerEndpoint{}
		host := strings.Trim(endpoint.Address, "[]")
		if ip := net.ParseIP(host); ip != nil {
			server.IpAddress = proto.String(ip.String())
		} else {
			server.DomainName = proto.String(host)
		}
		for _, transport := range transports {
			server.PortBindings = append(server.PortBindings, &appctlpb.PortBinding{
				Port: proto.Int32(int32(endpoint.Port)), Protocol: transport.Enum(),
			})
		}
		profile := &appctlpb.ClientProfile{
			ProfileName: proto.String(name),
			User:        &appctlpb.User{Name: proto.String(client.Email), Password: proto.String(client.Password)},
			Servers:     []*appctlpb.ServerEndpoint{server},
		}
		if network != "udp" {
			profile.Multiplexing = &appctlpb.MultiplexingConfig{Level: appctlpb.MultiplexingLevel_MULTIPLEXING_OFF.Enum()}
		}
		if err := appctlcommon.ValidateClientConfigSingleProfile(profile); err != nil {
			continue
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

func (s *SubService) genMieruMihomoProxies(inbound *model.Inbound, client model.Client) []map[string]any {
	var proxies []map[string]any
	for _, profile := range s.mieruProfiles(inbound, client) {
		for _, server := range profile.GetServers() {
			host := server.GetIpAddress()
			if host == "" {
				host = server.GetDomainName()
			}
			for _, binding := range server.GetPortBindings() {
				name := profile.GetProfileName()
				if len(server.GetPortBindings()) > 1 {
					name += " (" + binding.GetProtocol().String() + ")"
				}
				proxy := map[string]any{
					"name": name, "type": "mieru", "server": host, "port": int(binding.GetPort()),
					"transport": binding.GetProtocol().String(), "udp": true,
					"username": profile.GetUser().GetName(), "password": profile.GetUser().GetPassword(),
				}
				if binding.GetProtocol() == appctlpb.TransportProtocol_TCP {
					proxy["multiplexing"] = "MULTIPLEXING_OFF"
				}
				proxies = append(proxies, proxy)
			}
		}
	}
	return proxies
}
