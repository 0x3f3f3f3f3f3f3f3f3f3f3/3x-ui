package link

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/enfein/mieru/v3/pkg/appctl"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func parseMieru(raw string) (*ParseResult, error) {
	var profile *pb.ClientProfile
	var err error
	if strings.HasPrefix(raw, "mierus://") {
		if err = validateMieruSimpleURL(raw); err == nil {
			profile, err = appctl.URLToClientProfile(raw)
		}
	} else {
		var config *pb.ClientConfig
		config, err = appctl.URLToClientConfig(raw)
		if err == nil {
			if len(config.GetProfiles()) != 1 {
				return nil, fmt.Errorf("mieru outbound requires exactly one profile")
			}
			if err = validateMieruImportConfig(config); err == nil {
				profile = config.Profiles[0]
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return mieruProfileOutbound(profile)
}

func validateMieruSimpleURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Port() != "" || parsed.Path != "" || parsed.Fragment != "" {
		return fmt.Errorf("mieru endpoint must use the official port query")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return err
	}
	for key, values := range query {
		switch key {
		case "profile", "port", "protocol", "mtu", "multiplexing":
		default:
			return fmt.Errorf("unsupported mieru profile option %q", key)
		}
		if len(values) != 1 {
			return fmt.Errorf("mieru outbound requires a single %s value", key)
		}
	}
	if mode := query.Get("multiplexing"); mode != "" {
		switch mode {
		case "MULTIPLEXING_DEFAULT", "MULTIPLEXING_OFF", "MULTIPLEXING_LOW", "MULTIPLEXING_MIDDLE", "MULTIPLEXING_HIGH":
		default:
			return fmt.Errorf("invalid mieru multiplexing mode")
		}
	}
	return nil
}

func validateMieruImportConfig(config *pb.ClientConfig) error {
	if err := appctl.ValidateFullClientConfig(config); err != nil {
		return err
	}
	rest := proto.Clone(config).(*pb.ClientConfig)
	rest.Profiles = nil
	rest.ActiveProfile = nil
	rest.Socks5Port = nil
	rest.RpcPort = nil
	rest.HttpProxyPort = nil
	if proto.Size(rest) != 0 {
		return fmt.Errorf("mieru client options cannot be represented by a native outbound")
	}
	return nil
}

func parseMieruSubscriptionConfig(raw string) ([]Outbound, []string, error) {
	config := &pb.ClientConfig{}
	if err := protojson.Unmarshal([]byte(raw), config); err != nil {
		return nil, nil, err
	}
	if err := validateMieruImportConfig(config); err != nil {
		return nil, nil, err
	}
	var outbounds []Outbound
	var identities []string
	for _, profile := range config.Profiles {
		parsed, err := mieruProfileOutbound(profile)
		if err != nil {
			return nil, nil, err
		}
		outbounds = append(outbounds, parsed.Outbound)
		identities = append(identities, parsed.Identity)
	}
	return outbounds, identities, nil
}

func mieruProfileOutbound(profile *pb.ClientProfile) (*ParseResult, error) {
	if profile == nil || len(profile.Servers) != 1 || profile.Servers[0] == nil || len(profile.Servers[0].PortBindings) != 1 || profile.Servers[0].PortBindings[0] == nil {
		return nil, fmt.Errorf("mieru outbound requires one server and one port binding")
	}
	if err := appctlcommon.ValidateClientConfigSingleProfile(profile); err != nil {
		return nil, err
	}
	rest := proto.Clone(profile).(*pb.ClientProfile)
	rest.ProfileName = nil
	rest.User = nil
	rest.Servers = nil
	rest.Mtu = nil
	rest.Multiplexing = nil
	user := proto.Clone(profile.GetUser()).(*pb.User)
	user.Name = nil
	user.Password = nil
	server := profile.Servers[0]
	endpoint := proto.Clone(server).(*pb.ServerEndpoint)
	endpoint.IpAddress = nil
	endpoint.DomainName = nil
	endpoint.PortBindings = nil
	binding := server.PortBindings[0]
	port := proto.Clone(binding).(*pb.PortBinding)
	port.Port = nil
	port.Protocol = nil
	if proto.Size(rest) != 0 || proto.Size(user) != 0 || proto.Size(endpoint) != 0 || proto.Size(port) != 0 {
		return nil, fmt.Errorf("mieru profile has options that cannot be represented by a native outbound")
	}
	if (server.GetIpAddress() == "") == (server.GetDomainName() == "") {
		return nil, fmt.Errorf("mieru outbound requires one address")
	}
	address := server.GetDomainName()
	if address == "" {
		address = server.GetIpAddress()
	}
	mode := profile.GetMultiplexing().GetLevel().String()
	if mode == "MULTIPLEXING_DEFAULT" {
		mode = "MULTIPLEXING_LOW"
	}
	settings := map[string]any{"address": address, "port": binding.GetPort(), "username": profile.GetUser().GetName(), "password": profile.GetUser().GetPassword(), "transport": binding.GetProtocol().String(), "mtu": profile.GetMtu(), "multiplexing": mode}
	if profile.GetMtu() == 0 {
		settings["mtu"] = 1400
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	native := &conf.MieruClientConfig{}
	if err := json.Unmarshal(encoded, native); err != nil {
		return nil, err
	}
	if _, err := native.Build(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)
	return &ParseResult{Outbound: Outbound{"protocol": "mieru", "tag": profile.GetProfileName(), "settings": settings}, Identity: "mieru:" + hex.EncodeToString(sum[:])}, nil
}
