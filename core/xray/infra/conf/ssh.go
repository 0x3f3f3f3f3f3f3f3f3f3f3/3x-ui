package conf

import (
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	coressh "github.com/xtls/xray-core/proxy/ssh"
	"google.golang.org/protobuf/proto"
)

type SSHUser struct {
	Username   string   `json:"username"`
	PublicKeys []string `json:"publicKeys"`
	Password   string   `json:"password"`
	ClientID   string   `json:"clientId"`
	Email      string   `json:"email"`
	Level      uint32   `json:"level"`
}

type SSHServerConfig struct {
	HostKeyFile               string            `json:"hostKeyFile"`
	Users                     []*SSHUser        `json:"users"`
	AllowPassword             bool              `json:"allowPassword"`
	HandshakeTimeoutSeconds   uint32            `json:"handshakeTimeoutSeconds"`
	IdleTimeoutSeconds        uint32            `json:"idleTimeoutSeconds"`
	ChannelOpenTimeoutSeconds uint32            `json:"channelOpenTimeoutSeconds"`
	MaxAuthTries              uint32            `json:"maxAuthTries"`
	MaxConnections            uint32            `json:"maxConnections"`
	MaxConnectionsPerUser     uint32            `json:"maxConnectionsPerUser"`
	MaxChannelsPerConnection  uint32            `json:"maxChannelsPerConnection"`
	MaxChannels               uint32            `json:"maxChannels"`
	Reverse                   *SSHReverseConfig `json:"reverse"`
}

type SSHReverseConfig struct {
	Enabled       bool     `json:"enabled"`
	BindAddresses []string `json:"bindAddresses"`
	PortFrom      uint32   `json:"portFrom"`
	PortTo        uint32   `json:"portTo"`
	SourceCIDRs   []string `json:"sourceCIDRs"`
	MaxListeners  uint32   `json:"maxListeners"`
	AllowPortZero bool     `json:"allowPortZero"`
}

func (c *SSHServerConfig) Build() (proto.Message, error) {
	out := &coressh.ServerConfig{HostKeyFile: c.HostKeyFile, AllowPassword: c.AllowPassword, HandshakeTimeoutSeconds: c.HandshakeTimeoutSeconds, IdleTimeoutSeconds: c.IdleTimeoutSeconds, ChannelOpenTimeoutSeconds: c.ChannelOpenTimeoutSeconds, MaxAuthTries: c.MaxAuthTries, MaxConnections: c.MaxConnections, MaxConnectionsPerUser: c.MaxConnectionsPerUser, MaxChannelsPerConnection: c.MaxChannelsPerConnection, MaxChannels: c.MaxChannels}
	if r := c.Reverse; r != nil {
		out.Reverse = &coressh.ReverseConfig{Enabled: r.Enabled, BindAddresses: r.BindAddresses, PortFrom: r.PortFrom, PortTo: r.PortTo, SourceCidrs: r.SourceCIDRs, MaxListeners: r.MaxListeners, AllowPortZero: r.AllowPortZero}
	}
	if err := coressh.ValidateServerOptions(out); err != nil {
		return nil, err
	}
	for _, u := range c.Users {
		if u == nil || u.ClientID == "" || u.Email == "" || u.Username == "" {
			return nil, coressh.ErrConfiguration
		}
		out.Users = append(out.Users, &protocol.User{ClientId: u.ClientID, Email: u.Email, Level: u.Level, Account: serial.ToTypedMessage(&coressh.Account{Username: u.Username, PublicKeys: u.PublicKeys, Password: u.Password})})
	}
	return out, nil
}

type SSHClientConfig struct {
	Address                 string `json:"address"`
	Port                    uint32 `json:"port"`
	Username                string `json:"username"`
	PrivateKeyFile          string `json:"privateKeyFile"`
	Password                string `json:"password"`
	HostKey                 string `json:"hostKey"`
	HandshakeTimeoutSeconds uint32 `json:"handshakeTimeoutSeconds"`
	IdleTimeoutSeconds      uint32 `json:"idleTimeoutSeconds"`
}

func (c *SSHClientConfig) Build() (proto.Message, error) {
	if c.Address == "" || c.Port == 0 || c.Port > 65535 || c.Username == "" || c.HostKey == "" || c.PrivateKeyFile == "" && c.Password == "" {
		return nil, coressh.ErrConfiguration
	}
	return &coressh.ClientConfig{Address: c.Address, Port: c.Port, Username: c.Username, PrivateKeyFile: c.PrivateKeyFile, Password: c.Password, HostKey: c.HostKey, HandshakeTimeoutSeconds: c.HandshakeTimeoutSeconds, IdleTimeoutSeconds: c.IdleTimeoutSeconds}, nil
}
