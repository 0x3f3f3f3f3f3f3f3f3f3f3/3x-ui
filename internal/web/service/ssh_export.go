package service

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/infra/conf"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/net/idna"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type SSHClientExportResult struct {
	Config       string
	KnownHosts   string
	Instructions string
}

// OpenSSH does not decode Go Unicode escapes. Preserve validated UTF-8 and
// escape only the quoted-string delimiters understood by its config parser.
func quoteOpenSSH(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

// SSHClientExport emits native OpenSSH files using canonical business identity
// and public host trust. It never opens a client private-key file.
func SSHClientExport(inbound *model.Inbound, email, address string, port, endpoint int) (*SSHClientExportResult, error) {
	if inbound == nil || inbound.Protocol != model.SSH || port < 1 || port > 65535 || endpoint < 1 {
		return nil, fmt.Errorf("SSH export requires a native listener and valid endpoint")
	}
	var record model.ClientRecord
	if err := database.GetDB().Where("email = ?", email).First(&record).Error; err != nil {
		return nil, err
	}
	client := record.ToClient()
	if err := normalizeSSHCredentials(client); err != nil {
		return nil, err
	}
	if client.SSHUsername == "" {
		return nil, fmt.Errorf("SSH export requires canonical native authentication")
	}
	identity, err := uuid.Parse(record.StableID)
	if err != nil || identity == uuid.Nil || identity.String() != record.StableID {
		return nil, fmt.Errorf("SSH export requires canonical owner identity")
	}
	var options conf.SSHServerConfig
	if err := json.Unmarshal([]byte(inbound.Settings), &options); err != nil {
		return nil, err
	}
	options.Users = nil
	if _, err := options.Build(); err != nil {
		return nil, err
	}
	key, err := loadSSHHostKey(database.GetDB(), inbound.SSHHostKeyID)
	if err != nil {
		return nil, err
	}
	address = strings.Trim(address, "[]")
	if address == "" || strings.TrimSpace(address) != address || strings.ContainsAny(address, "\x00\r\n") {
		return nil, fmt.Errorf("SSH export requires a valid server address")
	}
	if net.ParseIP(address) == nil {
		address, err = idna.Lookup.ToASCII(address)
		if err != nil || len(address) > 253 {
			return nil, fmt.Errorf("SSH export requires a valid DNS name")
		}
		for _, label := range strings.Split(strings.TrimSuffix(address, "."), ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, fmt.Errorf("SSH export requires a valid DNS name")
			}
			for _, r := range label {
				if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-", r) {
					return nil, fmt.Errorf("SSH export requires a valid DNS name")
				}
			}
		}
	}
	keys := client.SSHAuthorizedKeys != ""
	password := options.AllowPassword && client.SSHPassword != ""
	if !keys && !password {
		return nil, fmt.Errorf("SSH export requires an allowed authentication method")
	}
	alias := fmt.Sprintf("x-ui-ssh-%s-%d-%d", record.StableID, inbound.Id, endpoint)
	privatePath := "~/.ssh/x-ui-business-" + record.StableID + ".key"
	var config strings.Builder
	fmt.Fprintf(&config, "# Native SSH business service; host fingerprint %s\nHost %s\n  HostName %s\n  Port %d\n  User %s\n", key.Fingerprint, alias, quoteOpenSSH(address), port, quoteOpenSSH(client.SSHUsername))
	config.WriteString("  StrictHostKeyChecking yes\n  UserKnownHostsFile \"~/.ssh/x-ui-ssh-known_hosts\"\n  GlobalKnownHostsFile /dev/null\n  UpdateHostKeys no\n  CheckHostIP no\n  IdentitiesOnly yes\n  IdentityAgent none\n  SessionType none\n  RequestTTY no\n  ExitOnForwardFailure yes\n")
	var methods []string
	if keys {
		methods = append(methods, "publickey")
		fmt.Fprintf(&config, "  IdentityFile %s\n  PubkeyAuthentication yes\n", quoteOpenSSH(privatePath))
	} else {
		config.WriteString("  PubkeyAuthentication no\n")
	}
	if password {
		methods = append(methods, "password")
		config.WriteString("  PasswordAuthentication yes\n")
	} else {
		config.WriteString("  PasswordAuthentication no\n")
	}
	fmt.Fprintf(&config, "  KbdInteractiveAuthentication no\n  PreferredAuthentications %s\n\n", strings.Join(methods, ","))
	known := knownhosts.Normalize(net.JoinHostPort(address, strconv.Itoa(port))) + " " + key.PublicKey + "\n"
	var instructions strings.Builder
	fmt.Fprintf(&instructions, "Host alias: %s\nHost fingerprint: %s\nSave x-ui-ssh.conf locally and install x-ui-ssh-known_hosts as ~/.ssh/x-ui-ssh-known_hosts.\n", alias, key.Fingerprint)
	if keys {
		fmt.Fprintf(&instructions, "Place your own matching business private key at %s with private file permissions. The panel exports public keys only.\n", privatePath)
	}
	if password {
		instructions.WriteString("Password authentication is allowed on this service. OpenSSH prompts interactively; the password is absent from these files and commands.\n")
	}
	fmt.Fprintf(&instructions, "TCP SOCKS forwarding:\n  ssh -F x-ui-ssh.conf -N -D 127.0.0.1:1080 %s\nLocal TCP forwarding (replace TARGET_HOST and TARGET_PORT):\n  ssh -F x-ui-ssh.conf -N -L 127.0.0.1:8080:TARGET_HOST:TARGET_PORT %s\n", alias, alias)
	if reverse := options.Reverse; reverse != nil && reverse.Enabled {
		bind := reverse.BindAddresses[0]
		if strings.Contains(bind, ":") {
			bind = "[" + bind + "]"
		}
		fmt.Fprintf(&instructions, "Authorized reverse TCP example (replace the client-local target):\n  ssh -F x-ui-ssh.conf -N -R %s:%d:127.0.0.1:8080 %s\nAllowed source CIDRs: %s\nThe SSH client chooses the final target; the server cannot verify that target.\n", bind, reverse.PortFrom, alias, strings.Join(reverse.SourceCIDRs, ", "))
	}
	return &SSHClientExportResult{Config: config.String(), KnownHosts: known, Instructions: instructions.String()}, nil
}
