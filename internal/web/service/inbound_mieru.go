package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/mieru"
)

type mieruInboundSettings struct {
	Network    string         `json:"network"`
	BridgePort int            `json:"bridgePort"`
	Clients    []model.Client `json:"clients"`
}

func normalizeMieruInbound(inbound, previous *model.Inbound) error {
	if inbound.Protocol != model.Mieru {
		return nil
	}
	var settings *mieruInboundSettings
	decoder := json.NewDecoder(strings.NewReader(inbound.Settings))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil || settings == nil {
		return errors.New("invalid mieru inbound settings")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("invalid trailing mieru settings")
	}
	var old mieruInboundSettings
	if previous != nil && previous.Protocol == model.Mieru {
		if err := json.Unmarshal([]byte(previous.Settings), &old); err != nil {
			return errors.New("invalid stored mieru settings")
		}
	}
	if settings.Network == "" {
		settings.Network = old.Network
	}
	if settings.Network == "" {
		settings.Network = "tcp"
	}
	if settings.Network != "tcp" && settings.Network != "udp" && settings.Network != "both" {
		return errors.New("mieru transport must be tcp, udp or both")
	}
	if inbound.Port < 1 || inbound.Port > 65535 {
		return errors.New("invalid mieru listener port")
	}
	listen, validListen := canonicalManagedListen(inbound.Listen)
	if !validListen {
		return errors.New("mieru listen address must be a literal IP")
	}
	inbound.Listen = listen
	if settings.BridgePort == 0 {
		settings.BridgePort = old.BridgePort
	}
	if settings.BridgePort == 0 {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			return errors.New("cannot allocate a local mieru routing bridge")
		}
		settings.BridgePort = listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
	}
	if settings.BridgePort < 1 || settings.BridgePort > 65535 || settings.BridgePort == inbound.Port {
		return errors.New("invalid or overlapping mieru bridge port")
	}
	bindings := make([]mieru.Client, 0, len(settings.Clients))
	for _, client := range settings.Clients {
		bindings = append(bindings, mieru.Client{PolicyID: uuid.NewString(), Username: client.Email, Password: client.Password})
	}
	if err := mieru.ValidateClients(bindings); err != nil {
		return errors.New("invalid or duplicate mieru client credentials")
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	inbound.Settings = string(encoded)
	inbound.Sniffing = ""
	return nil
}

func mieruClientBinding(client model.Client, policyID string) (mieru.Client, error) {
	result := mieru.Client{PolicyID: policyID, Username: client.Email, Password: client.Password}
	if err := mieru.ValidateClients([]mieru.Client{result}); err != nil {
		return result, errors.New("invalid mieru client username or password")
	}
	return result, nil
}
