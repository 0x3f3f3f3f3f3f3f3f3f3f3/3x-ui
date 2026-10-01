package conf

import (
	"errors"
	"strings"

	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/mieru"
	"google.golang.org/protobuf/proto"
)

type MieruUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
	ClientID string `json:"clientId"`
	Email    string `json:"email"`
	Level    uint32 `json:"level"`
}

type MieruServerConfig struct {
	Users                   []MieruUser `json:"users"`
	Transport               string      `json:"transport"`
	MTU                     uint32      `json:"mtu"`
	UserHintRequired        bool        `json:"userHintRequired"`
	MaxConnections          uint32      `json:"maxConnections"`
	HandshakeTimeoutSeconds uint32      `json:"handshakeTimeoutSeconds"`
}

func mieruTransport(value string) (string, error) {
	if value == "" {
		return "TCP", nil
	}
	if value != "TCP" && value != "UDP" {
		return "", errors.New("mieru transport must be TCP or UDP")
	}
	return value, nil
}

func (c *MieruServerConfig) Build() (proto.Message, error) {
	transport, err := mieruTransport(c.Transport)
	if err != nil {
		return nil, err
	}
	if c.MTU != 0 && (c.MTU < 1280 || c.MTU > 1500) {
		return nil, errors.New("mieru mtu must be between 1280 and 1500")
	}
	config := &mieru.ServerConfig{
		Transport: transport, Mtu: c.MTU, UserHintRequired: c.UserHintRequired,
		MaxConnections: c.MaxConnections, HandshakeTimeoutSeconds: c.HandshakeTimeoutSeconds,
	}
	seen := make(map[string]bool)
	owners := make(map[string]string)
	for _, user := range c.Users {
		if err := mieru.ValidateCredentials(user.Username, user.Password); err != nil {
			return nil, err
		}
		if seen[user.Username] || user.ClientID == "" || len(user.ClientID) > 128 || user.Email == "" || strings.TrimSpace(user.ClientID) != user.ClientID || strings.ContainsAny(user.ClientID+user.Email, "\x00\r\n") {
			return nil, errors.New("mieru user requires unique credentials and a trusted canonical identity")
		}
		if owner, exists := owners[user.Email]; exists && owner != user.ClientID {
			return nil, errors.New("mieru email has conflicting stable owners")
		}
		seen[user.Username], owners[user.Email] = true, user.ClientID
		config.Users = append(config.Users, &protocol.User{
			Email: user.Email, ClientId: user.ClientID, Level: user.Level,
			Account: serial.ToTypedMessage(&mieru.Account{Username: user.Username, Password: user.Password}),
		})
	}
	return config, nil
}

type MieruClientConfig struct {
	Address      *Address `json:"address"`
	Port         uint32   `json:"port"`
	Username     string   `json:"username"`
	Password     string   `json:"password"`
	Transport    string   `json:"transport"`
	Multiplexing string   `json:"multiplexing"`
	MTU          uint32   `json:"mtu"`
}

func (c *MieruClientConfig) Build() (proto.Message, error) {
	transport, err := mieruTransport(c.Transport)
	if err != nil {
		return nil, err
	}
	if err := mieru.ValidateCredentials(c.Username, c.Password); err != nil {
		return nil, err
	}
	if c.Address == nil || c.Address.String() == "" || c.Port == 0 || c.Port > 65535 {
		return nil, errors.New("mieru outbound requires a server address and valid port")
	}
	if c.MTU != 0 && (c.MTU < 1280 || c.MTU > 1500) {
		return nil, errors.New("mieru mtu must be between 1280 and 1500")
	}
	mode := c.Multiplexing
	if mode == "" {
		mode = "MULTIPLEXING_LOW"
	}
	switch mode {
	case "MULTIPLEXING_OFF", "MULTIPLEXING_LOW", "MULTIPLEXING_MIDDLE", "MULTIPLEXING_HIGH":
	default:
		return nil, errors.New("invalid mieru multiplexing mode")
	}
	return &mieru.ClientConfig{
		Address: c.Address.Build(), Port: c.Port, Username: c.Username, Password: c.Password,
		Transport: transport, Multiplexing: mode, Mtu: c.MTU,
	}, nil
}
