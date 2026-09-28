package service

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/sshoutbound"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func sshOutboundTestConfig(t *testing.T) sshoutbound.Config {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.MarshalPrivateKey(private, "isolated settings test")
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return sshoutbound.Config{Address: "upstream.invalid", Port: 22, User: "forward", PrivateKey: string(pem.EncodeToMemory(key)), HostKey: string(ssh.MarshalAuthorizedKey(host))}
}

func TestSSHOutboundSaveAndPreviewArePure(t *testing.T) {
	for _, protocol := range []string{"ssh", "SSH", "Ssh"} {
		t.Run(protocol, func(t *testing.T) { sshOutboundSaveAndPreview(t, protocol) })
	}
}

func sshOutboundSaveAndPreview(t *testing.T, protocol string) {
	t.Helper()
	setupSettingTestDB(t)
	address := productionSSHAddress(t)
	_, port, _ := net.SplitHostPort(address)
	t.Setenv("XUI_SSH_UPSTREAM_BRIDGE_PORT", port)
	svc := &XrayService{}
	isManuallyStopped.Store(false)
	t.Cleanup(func() {
		_ = svc.StopXray()
		isManuallyStopped.Store(false)
		isNeedXrayRestart.Store(false)
		xrayState.holdBack("")
	})
	settings := sshOutboundTestConfig(t)
	authored := map[string]any{"tag": "upstream", "protocol": protocol, "settings": settings}
	template, err := json.Marshal(map[string]any{"inbounds": []any{}, "outbounds": []any{authored}})
	if err != nil {
		t.Fatal(err)
	}
	settingsService := &XraySettingService{}
	if err := settingsService.SaveXraySetting(string(template)); err != nil {
		t.Fatalf("valid SSH outbound could not be saved: %v", err)
	}
	before, err := settingsService.GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.OutboundConfigs, second.OutboundConfigs) {
		t.Fatal("preview rotated bridge credentials")
	}
	var compiled []json.RawMessage
	if err := json.Unmarshal(first.OutboundConfigs, &compiled); err != nil || len(compiled) != 1 {
		t.Fatalf("compiled outbounds count=%d err=%v", len(compiled), err)
	}
	if err := xray.ValidateOutboundConfig(compiled[0]); err != nil {
		t.Fatalf("compiled outbound is not accepted by Xray: %v", err)
	}
	var meta struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
		Settings struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(compiled[0], &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Tag != "upstream" || meta.Protocol != "socks" || net.JoinHostPort(meta.Settings.Address, strconv.Itoa(meta.Settings.Port)) != address {
		t.Fatal("compiled SSH route lost its tag or private bridge endpoint")
	}
	if bytes.Contains(compiled[0], []byte("privateKey")) || bytes.Contains(compiled[0], []byte(strings.Split(settings.PrivateKey, "\n")[1])) {
		t.Fatal("compiled core config leaked upstream private key material")
	}
	probe, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("save/preview bound private bridge: %v", err)
	}
	_ = probe.Close()
	authored["streamSettings"] = map[string]any{"security": "tls"}
	invalid, _ := json.Marshal(map[string]any{"inbounds": []any{}, "outbounds": []any{authored}})
	if err := settingsService.SaveXraySetting(string(invalid)); err == nil {
		t.Fatal("unsupported SSH transport saved")
	}
	after, err := settingsService.GetXrayConfigTemplate()
	if err != nil || before != after {
		t.Fatalf("rejected SSH config changed stored template: %v", err)
	}
	xrayState.holdBack("")
	t.Setenv("XUI_SSH_UPSTREAM_BRIDGE_PORT", "invalid")
	if err := svc.RestartXray(false); err == nil || !strings.Contains(err.Error(), "XUI_SSH_UPSTREAM_BRIDGE_PORT") {
		t.Fatalf("invalid runtime bridge port was not rejected: %v", err)
	}
	if !strings.Contains(xrayState.heldBackReason(), "XUI_SSH_UPSTREAM_BRIDGE_PORT") {
		t.Fatal("invalid SSH runtime configuration was absent from held-back status")
	}
}
