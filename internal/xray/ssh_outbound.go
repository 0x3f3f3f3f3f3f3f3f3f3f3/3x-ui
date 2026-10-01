package xray

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/xtls/xray-core/infra/conf"
	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

// ValidateNativeSSHOutbounds applies the business-key boundary even when the
// saved template came from restore or another path that bypassed form validation.
func ValidateNativeSSHOutbounds(raw []byte) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var outbounds []json.RawMessage
	if err := json.Unmarshal(raw, &outbounds); err != nil {
		return err
	}
	for _, outbound := range outbounds {
		if err := validateNativeSSHOutbound(outbound); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeSSHOutbound(raw []byte) error {
	var outbound struct {
		Protocol       string          `json:"protocol"`
		Settings       json.RawMessage `json:"settings"`
		StreamSettings json.RawMessage `json:"streamSettings"`
		Mux            json.RawMessage `json:"mux"`
	}
	if err := json.Unmarshal(raw, &outbound); err != nil {
		return err
	}
	if !strings.EqualFold(outbound.Protocol, "ssh") {
		return nil
	}
	if len(outbound.Mux) > 0 && string(outbound.Mux) != "null" {
		var mux struct {
			Enabled         bool   `json:"enabled"`
			Concurrency     int    `json:"concurrency"`
			XUDPConcurrency int    `json:"xudpConcurrency"`
			XUDPProxyUDP443 string `json:"xudpProxyUDP443"`
		}
		if err := json.Unmarshal(outbound.Mux, &mux); err != nil {
			return err
		}
		if mux.Enabled || mux.XUDPConcurrency != 0 || mux.XUDPProxyUDP443 != "" {
			return fmt.Errorf("SSH does not support global mux or UDP")
		}
	}
	var stream map[string]json.RawMessage
	if len(outbound.StreamSettings) > 0 {
		if err := json.Unmarshal(outbound.StreamSettings, &stream); err != nil {
			return err
		}
	}
	for key := range stream {
		if key != "network" && key != "security" && key != "sockopt" {
			return fmt.Errorf("SSH does not support transport wrappers")
		}
	}
	var network, security string
	if raw, ok := stream["network"]; ok {
		if err := json.Unmarshal(raw, &network); err != nil {
			return err
		}
	}
	if raw, ok := stream["security"]; ok {
		if err := json.Unmarshal(raw, &security); err != nil {
			return err
		}
	}
	if network != "" && network != "tcp" && network != "raw" || security != "" && security != "none" {
		return fmt.Errorf("SSH supports native TCP without TLS or another transport wrapper")
	}
	var settings conf.SSHClientConfig
	decoder := json.NewDecoder(bytes.NewReader(outbound.Settings))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return err
	}
	if _, err := settings.Build(); err != nil {
		return err
	}
	if len(settings.Address) > 253 || len(settings.Username) > 256 || len(settings.Password) > 1024 || len(settings.HostKey) > 16384 || settings.HandshakeTimeoutSeconds > 120 || settings.IdleTimeoutSeconds > 86400 {
		return fmt.Errorf("SSH outbound exceeds native limits")
	}
	for _, value := range []string{settings.Address, settings.Username} {
		if strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return fmt.Errorf("SSH address and username cannot contain whitespace or control characters")
		}
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(settings.HostKey))
	if err != nil || len(options) != 0 || strings.TrimSpace(string(rest)) != "" {
		return fmt.Errorf("SSH outbound requires a valid pinned public host key")
	}
	if _, certificate := key.(*ssh.Certificate); certificate {
		return fmt.Errorf("SSH host certificates are unsupported")
	}
	if settings.PrivateKeyFile != "" {
		return validateSSHBusinessPrivateKey(settings.PrivateKeyFile)
	}
	return nil
}

func validateSSHBusinessPrivateKey(path string) error {
	dbFolder, err := filepath.Abs(config.GetDBFolderPath())
	if err != nil {
		return err
	}
	business := filepath.Join(dbFolder, "native-ssh", "outbound")
	if !filepath.IsAbs(path) || path != filepath.Clean(path) || filepath.Dir(path) != business {
		return fmt.Errorf("SSH outbound privateKeyFile must be directly inside the configured private native-ssh/outbound business directory")
	}
	root, err := os.OpenRoot(dbFolder)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"native-ssh", filepath.Join("native-ssh", "outbound")} {
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("SSH outbound business directories must be real private 0700 directories")
		}
	}
	keys, err := root.OpenRoot(filepath.Join("native-ssh", "outbound"))
	if err != nil {
		return err
	}
	defer keys.Close()
	name := filepath.Base(path)
	info, err := keys.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 65536 {
		return fmt.Errorf("SSH outbound business key must be a private 0600 regular file")
	}
	file, err := keys.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 {
		return fmt.Errorf("SSH outbound business key exceeds native size limits")
	}
	if _, err := ssh.ParsePrivateKey(data); err != nil {
		return fmt.Errorf("SSH outbound business private key is invalid or encrypted")
	}
	return nil
}
