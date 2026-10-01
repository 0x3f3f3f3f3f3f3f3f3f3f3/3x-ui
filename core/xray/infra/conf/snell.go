package conf

import (
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/snell"
	"google.golang.org/protobuf/proto"
)

type SnellServerConfig struct {
	Version  uint32 `json:"version"`
	PSK      string `json:"psk"`
	ClientID string `json:"clientId"`
	Email    string `json:"email"`
	Level    uint32 `json:"level"`
	Obfs     string `json:"obfs"`
	Mode     string `json:"mode"`
	QUIC     bool   `json:"quic"`
}

func (c *SnellServerConfig) Build() (proto.Message, error) {
	v := &snell.ServerConfig{Version: c.Version, User: &protocol.User{ClientId: c.ClientID, Email: c.Email, Level: c.Level, Account: serial.ToTypedMessage(&snell.Account{Psk: c.PSK})}, Obfs: c.Obfs, Mode: c.Mode, Quic: c.QUIC}
	return v, snell.ValidateServer(v)
}

type SnellClientConfig struct {
	Version  uint32   `json:"version"`
	PSK      string   `json:"psk"`
	Address  *Address `json:"address"`
	Port     uint32   `json:"port"`
	Obfs     string   `json:"obfs"`
	ObfsHost string   `json:"obfsHost"`
	ObfsURI  string   `json:"obfsUri"`
	Mode     string   `json:"mode"`
	Reuse    bool     `json:"reuse"`
	QUIC     bool     `json:"quic"`
}

func (c *SnellClientConfig) Build() (proto.Message, error) {
	v := &snell.ClientConfig{Version: c.Version, Psk: c.PSK, Port: c.Port, Obfs: c.Obfs, ObfsHost: c.ObfsHost, ObfsUri: c.ObfsURI, Mode: c.Mode, Reuse: c.Reuse, Quic: c.QUIC}
	if c.Address != nil {
		v.Address = c.Address.Build()
	}
	return v, snell.ValidateClient(v)
}
