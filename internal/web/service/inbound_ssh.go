package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/sshtunnel"
)

type sshInboundSettings struct {
	HostKey    string         `json:"hostKey"`
	BridgePort int            `json:"bridgePort"`
	Clients    []model.Client `json:"clients"`
}

func normalizeSSHInbound(inbound *model.Inbound, previous string) error {
	if inbound.Protocol != model.SSH {
		return nil
	}
	var settings, old sshInboundSettings
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return errors.New("invalid SSH inbound settings")
	}
	if previous != "" {
		if err := json.Unmarshal([]byte(previous), &old); err != nil {
			return errors.New("invalid stored SSH inbound settings")
		}
	}
	if settings.HostKey == "" {
		settings.HostKey = old.HostKey
	}
	if settings.HostKey == "" {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		block, err := ssh.MarshalPrivateKey(key, "3x-ui managed SSH host")
		if err != nil {
			return err
		}
		settings.HostKey = string(pem.EncodeToMemory(block))
	}
	if _, err := ssh.ParsePrivateKey([]byte(settings.HostKey)); err != nil {
		return errors.New("invalid SSH host private key")
	}
	if settings.BridgePort == 0 {
		settings.BridgePort = old.BridgePort
	}
	if settings.BridgePort == 0 {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			return errors.New("cannot allocate a local SSH routing bridge")
		}
		settings.BridgePort = listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
	}
	if settings.BridgePort < 1 || settings.BridgePort > 65535 || settings.BridgePort == inbound.Port {
		return errors.New("invalid or overlapping SSH bridge port")
	}
	if inbound.Port < 1 || inbound.Port > 65535 {
		return errors.New("invalid SSH listener port")
	}
	if inbound.Listen != "" && net.ParseIP(strings.Trim(inbound.Listen, "[]")) == nil {
		return errors.New("SSH listen address must be a literal IP")
	}
	for _, client := range settings.Clients {
		if _, err := sshClientBinding(client, uuid.NewString()); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	inbound.Settings = string(encoded)
	return nil
}

func sshClientBinding(client model.Client, policyID string) (sshtunnel.Client, error) {
	result := sshtunnel.Client{PolicyID: policyID, Username: client.Email}
	if client.SSH == nil {
		return result, errors.New("SSH client requires explicit public keys and target permissions")
	}
	if len(client.SSH.PublicKeys) > 16 || len(client.SSH.Targets) > 256 || len(client.SSH.Reverse) > 16 {
		return result, errors.New("SSH client exceeds credential or permission limits")
	}
	for _, text := range client.SSH.PublicKeys {
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(text))
		if err != nil || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
			return result, errors.New("invalid SSH public key; authorized_keys options are not supported")
		}
		result.PublicKeys = append(result.PublicKeys, key)
	}
	for _, rule := range client.SSH.Targets {
		result.Targets = append(result.Targets, sshtunnel.TargetRule{Host: rule.Host, Port: rule.Port})
	}
	for _, rule := range client.SSH.Reverse {
		result.Reverse = append(result.Reverse, sshtunnel.ReverseRule{Address: rule.Address, Port: rule.Port})
	}
	if err := sshtunnel.ValidateClients([]sshtunnel.Client{result}); err != nil {
		return result, errors.New("invalid SSH client identity, key or access rule")
	}
	return result, nil
}

func sshListenAddress(inbound *model.Inbound) string {
	host := strings.Trim(inbound.Listen, "[]")
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(inbound.Port))
}
