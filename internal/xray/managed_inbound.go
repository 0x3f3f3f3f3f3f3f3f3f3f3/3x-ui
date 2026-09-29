package xray

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"

	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/trojan"
	"google.golang.org/protobuf/encoding/protowire"
)

var ErrManagedInbound = errors.New("invalid private managed inbound")

func buildAPIInbound(raw []byte) (*core.InboundHandlerConfig, error) {
	var inbound conf.InboundDetourConfig
	if err := json.Unmarshal(raw, &inbound); err != nil {
		return nil, err
	}
	if !strings.EqualFold(inbound.Protocol, "trojan") || inbound.Settings == nil {
		return inbound.Build()
	}
	var mode struct {
		Managed bool `json:"managed"`
	}
	if err := json.Unmarshal(*inbound.Settings, &mode); err != nil {
		return nil, ErrManagedInbound
	}
	if !mode.Managed {
		return inbound.Build()
	}
	var settings struct {
		Managed bool `json:"managed"`
		Clients []struct {
			Email    string  `json:"email"`
			Password string  `json:"password"`
			Level    *uint32 `json:"level"`
		} `json:"clients"`
	}
	decoder := json.NewDecoder(bytes.NewReader(*inbound.Settings))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil || len(settings.Clients) == 0 || inbound.ListenOn == nil {
		return nil, ErrManagedInbound
	}
	address, err := netip.ParseAddr(inbound.ListenOn.String())
	if err != nil || !address.IsLoopback() || address.Zone() != "" {
		return nil, ErrManagedInbound
	}
	server := &trojan.ServerConfig{}
	emails := make(map[string]bool, len(settings.Clients))
	passwords := make(map[string]bool, len(settings.Clients))
	for _, client := range settings.Clients {
		if client.Level == nil || client.Password == "" || client.Email == "" || emails[client.Email] || passwords[client.Password] {
			return nil, ErrManagedInbound
		}
		emails[client.Email] = true
		passwords[client.Password] = true
		server.Users = append(server.Users, &protocol.User{Email: client.Email, Level: *client.Level, Account: serial.ToTypedMessage(&trojan.Account{Password: client.Password})})
	}
	// Pinned managed-core patch 0002 defines trojan.ServerConfig.managed as field 3.
	server.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 3, protowire.VarintType), 1))
	empty := json.RawMessage(`{}`)
	inbound.Settings = &empty
	config, err := inbound.Build()
	if err != nil {
		return nil, err
	}
	config.ProxySettings = serial.ToTypedMessage(server)
	return config, nil
}
