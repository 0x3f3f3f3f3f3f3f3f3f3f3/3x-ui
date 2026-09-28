package service

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestSSHConfigPreviewKeepsExistingFlow(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to exercise real SSH ingress during config preview")
	}
	setupConflictDB(t)
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	t.Setenv("XUI_LOG_FOLDER", filepath.Join(binDir, "logs"))
	if err := os.Symlink(binary, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	var accepted atomic.Int64
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	_, apiPortText, _ := net.SplitHostPort(productionSSHAddress(t))
	apiPort, _ := strconv.Atoi(apiPortText)
	template := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"api":       map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"stats":     map[string]any{},
		"inbounds":  []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": apiPort, "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}},
		"outbounds": []any{map[string]any{"tag": "echo", "protocol": "freedom", "settings": map[string]any{"redirect": target.Addr().String()}}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}}},
	}
	encoded, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	settingsService := &XraySettingService{}
	if err := settingsService.SaveXraySetting(string(encoded)); err != nil {
		t.Fatal(err)
	}
	svc := &XrayService{}
	isManuallyStopped.Store(false)
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: svc.GetXrayAPIPort, SetNeedRestart: svc.SetToNeedRestart, SSHChanged: NotifySSHChange}))
	t.Cleanup(func() {
		_ = svc.StopXray()
		runtime.SetManager(nil)
		isManuallyStopped.Store(false)
		isNeedXrayRestart.Store(false)
	})
	credentials := sshOutboundTestConfig(t)
	signer, err := ssh.ParsePrivateKey([]byte(credentials.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	client := model.Client{Email: "preview-survivor", Enable: true, TotalGB: 65536, SSH: &model.SSHClient{PublicKeys: []string{credentials.HostKey}, Targets: []model.SSHTarget{{Host: "route.invalid", Port: 443}}}}
	inboundSettings, err := json.Marshal(map[string]any{"clients": []model.Client{client}})
	if err != nil {
		t.Fatal(err)
	}
	address := productionSSHAddress(t)
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	inbound := &model.Inbound{Protocol: model.SSH, Enable: true, Listen: "127.0.0.1", Port: port, Settings: string(inboundSettings)}
	if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, address)
	var stored sshInboundSettings
	if err := json.Unmarshal([]byte(inbound.Settings), &stored); err != nil {
		t.Fatal(err)
	}
	host, err := ssh.ParsePrivateKey([]byte(stored.HostKey))
	if err != nil {
		t.Fatal(err)
	}
	connection := productionSSHDial(t, address, client.Email, signer, host.PublicKey())
	flow, err := connection.Dial("tcp", "route.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	productionSSHEcho(t, flow)
	template["log"] = map[string]any{"loglevel": "error"}
	encoded, err = json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	if err := settingsService.SaveXraySetting(string(encoded)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetXrayConfig(); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		productionSSHEcho(t, flow)
		time.Sleep(50 * time.Millisecond)
	}
	if accepted.Load() != 1 {
		t.Fatalf("preview replaced the existing data path: target connections=%d", accepted.Load())
	}
	cfg, plan, err := svc.buildXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	release := stageSSHRuntime(cfg)
	defer release()
	currentXrayProcess().SetConfig(cfg)
	until = time.Now().Add(time.Second)
	for time.Now().Before(until) {
		productionSSHEcho(t, flow)
		time.Sleep(50 * time.Millisecond)
	}
	plan.apply(cfg)
	release()
	productionSSHEcho(t, flow)
	if accepted.Load() != 1 {
		t.Fatalf("staged application replaced the existing data path: target connections=%d", accepted.Load())
	}
}
