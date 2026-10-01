package service

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/mieru"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// MieruClientProfile projects the stored native credential pair to the official
// profile. Display labels and credentials for other protocols are independent.
func MieruClientProfile(inbound *model.Inbound, client model.Client, address string, port int, name string) (*pb.ClientProfile, error) {
	if inbound == nil || inbound.Protocol != model.Mieru {
		return nil, fmt.Errorf("mieru listener required")
	}
	if err := mieru.ValidateCredentials(client.MieruUsername, client.MieruPassword); err != nil {
		return nil, err
	}
	var settings conf.MieruServerConfig
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return nil, err
	}
	settings.Users = nil
	if _, err := settings.Build(); err != nil {
		return nil, err
	}
	address = strings.Trim(address, "[]")
	if address == "" || strings.TrimSpace(address) != address || strings.ContainsAny(address, "\x00\r\n") || port < 1 || port > 65535 {
		return nil, fmt.Errorf("mieru profile requires a server address and valid port")
	}
	if name == "" {
		name = client.Email
	}
	if name == "" {
		return nil, fmt.Errorf("mieru profile requires a name")
	}
	transport := pb.TransportProtocol_TCP
	if settings.Transport == "UDP" {
		transport = pb.TransportProtocol_UDP
	}
	mtu := settings.MTU
	if mtu == 0 {
		mtu = 1400
	}
	server := &pb.ServerEndpoint{PortBindings: []*pb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: transport.Enum()}}}
	if net.ParseIP(address) != nil {
		server.IpAddress = proto.String(address)
	} else {
		server.DomainName = proto.String(address)
	}
	return &pb.ClientProfile{ProfileName: proto.String(name), User: &pb.User{Name: proto.String(client.MieruUsername), Password: proto.String(client.MieruPassword)}, Servers: []*pb.ServerEndpoint{server}, Mtu: proto.Int32(int32(mtu)), Multiplexing: &pb.MultiplexingConfig{Level: pb.MultiplexingLevel_MULTIPLEXING_LOW.Enum()}}, nil
}
