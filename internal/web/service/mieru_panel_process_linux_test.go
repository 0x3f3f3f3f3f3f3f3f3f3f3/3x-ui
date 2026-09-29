//go:build linux

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type mieruPanelProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func TestMieruPanelProcessRestart(t *testing.T) {
	testMieruPanelProcessRestart(t, false)
}

func TestMieruPanelProcessRestart_Postgres(t *testing.T) {
	testMieruPanelProcessRestart(t, true)
}

func testMieruPanelProcessRestart(t *testing.T, postgres bool) {
	t.Helper()
	panelBinary, coreBinary := os.Getenv("XUI_E2E_PANEL"), os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if panelBinary == "" || coreBinary == "" {
		t.Skip("set XUI_E2E_PANEL and XUI_MANAGED_XRAY_E2E_BINARY for actual panel process acceptance")
	}
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			for _, shutdown := range []struct {
				name   string
				signal syscall.Signal
			}{{"SIGTERM", syscall.SIGTERM}, {"SIGKILL", syscall.SIGKILL}} {
				t.Run(shutdown.name, func(t *testing.T) {
					directory, password, httpPort, address, apiAddress := prepareMieruPanelProcess(t, postgres, panelBinary, coreBinary)
					panel := startMieruPanelProcess(t, panelBinary, directory, 1)
					httpAPI := newMieruPanelHTTP(t, httpPort, password)
					users := []model.Client{
						{Email: "process-depleted", Password: uuid.NewString(), Enable: true},
						{Email: "process-healthy", Password: uuid.NewString(), Enable: true},
					}
					settings, err := json.Marshal(map[string]any{"network": underlay, "clients": users})
					if err != nil {
						t.Fatal(err)
					}
					inbound := model.Inbound{Protocol: model.Mieru, Enable: true, Listen: "127.0.0.1", Port: int(address.Port()), Settings: string(settings)}
					httpAPI.call(t, "/panel/api/inbounds/add", inbound, &inbound)
					httpAPI.call(t, "/panel/api/server/restartXrayService", struct{}{}, nil)
					httpAPI.waitMieru(t, inbound.Id)
					for i, user := range users {
						var policy ClientPolicy
						path := "/panel/api/clients/policy/" + user.Email
						httpAPI.call(t, path, nil, &policy)
						policy.Multiplier = []string{"2", "1.5"}[i]
						policy.UploadBps, policy.DownloadBps = 65536, 32768
						httpAPI.call(t, path, policy.ClientPolicyUpdate, &policy)
					}
					depleted := productionMieruClient(t, underlay, address, users[0])
					healthy := productionMieruClient(t, underlay, address, users[1])
					depletedFlows := openProductionMieruFlows(t, depleted)
					healthyFlows := openProductionMieruFlows(t, healthy)
					httpAPI.requireUsage(t, users[0].Email, "44", "44", "176")
					httpAPI.requireUsage(t, users[1].Email, "44", "44", "132")
					var hydrated struct {
						Client struct {
							UUID  string `json:"uuid"`
							SubID string `json:"subId"`
						} `json:"client"`
					}
					httpAPI.call(t, "/panel/api/clients/get/"+users[0].Email, nil, &hydrated)
					httpAPI.call(t, "/panel/api/clients/update/"+users[0].Email, map[string]any{
						"email": users[0].Email, "id": hydrated.Client.UUID, "subId": hydrated.Client.SubID,
						"password": users[0].Password, "enable": true, "totalGB": 176,
					}, nil)
					requireProductionMieruClosed(t, depletedFlows)
					requireProductionMieruDenied(t, depleted)
					for _, flow := range healthyFlows {
						flow.echo(t)
					}
					before := []ClientPolicy{
						httpAPI.requireUsage(t, users[0].Email, "44", "44", "176"),
						httpAPI.requireUsage(t, users[1].Email, "88", "88", "264"),
					}
					if before[0].Usage.Unlimited || before[0].Usage.Quota != "176" || before[0].Usage.Remaining != "0" {
						t.Fatalf("public API did not persist depleted quota: %+v", before[0].Usage)
					}
					panel.stop(t, shutdown.signal)
					requireMieruPanelListenersReleased(t, underlay, address, apiAddress)
					for _, flow := range healthyFlows {
						_ = flow.conn.Close()
					}
					_ = depleted.Stop()
					_ = healthy.Stop()
					restarted := startMieruPanelProcess(t, panelBinary, directory, 2)
					httpAPI = newMieruPanelHTTP(t, httpPort, password)
					httpAPI.waitMieru(t, inbound.Id)
					for i, user := range users {
						var after ClientPolicy
						httpAPI.call(t, "/panel/api/clients/policy/"+user.Email, nil, &after)
						if after != before[i] {
							t.Fatalf("panel restart changed %s policy or usage: before=%+v after=%+v", user.Email, before[i], after)
						}
					}
					requireProductionMieruDenied(t, productionMieruClient(t, underlay, address, users[0]))
					openProductionMieruFlows(t, productionMieruClient(t, underlay, address, users[1]))
					httpAPI.requireUsage(t, users[0].Email, "44", "44", "176")
					httpAPI.requireUsage(t, users[1].Email, "132", "132", "396")
					restarted.stop(t, syscall.SIGTERM)
					requireMieruPanelListenersReleased(t, underlay, address, apiAddress)
				})
			}
		})
	}
}

func prepareMieruPanelProcess(t *testing.T, postgres bool, panelBinary, coreBinary string) (string, string, uint16, netip.AddrPort, netip.AddrPort) {
	t.Helper()
	directory := t.TempDir()
	for key, value := range map[string]string{
		"XUI_DB_FOLDER": filepath.Join(directory, "db"), "XUI_BIN_FOLDER": filepath.Join(directory, "bin"),
		"XUI_LOG_FOLDER": filepath.Join(directory, "log"), "XUI_DEBUG": "false", "XUI_DB_TYPE": "sqlite", "XUI_DB_DSN": "",
	} {
		t.Setenv(key, value)
	}
	if postgres {
		managedUsagePostgresSchema(t)
	}
	for _, folder := range []string{"db", "bin", "log"} {
		if err := os.MkdirAll(filepath.Join(directory, folder), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(coreBinary, filepath.Join(directory, "bin", xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	httpPort := netip.MustParseAddrPort(productionSSHAddress(t)).Port()
	t.Setenv("XUI_PORT", strconv.Itoa(int(httpPort)))
	password := uuid.NewString()
	setup := exec.CommandContext(t.Context(), panelBinary, "setting", "-username", "mieru-process", "-password", password, "-port", strconv.Itoa(int(httpPort)), "-listenIP", "127.0.0.1", "-webBasePath", "/")
	setup.Dir = directory
	if err := setup.Run(); err != nil {
		t.Fatalf("initialize actual panel credentials: %v", err)
	}
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	tcpTarget, udpTarget := productionMieruEchoTarget(t, "tcp"), productionMieruEchoTarget(t, "udp")
	apiAddress := netip.MustParseAddrPort(productionSSHAddress(t))
	template := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"api": map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}}, "stats": map[string]any{},
		"inbounds": []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": apiAddress.Port(), "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}},
		"outbounds": []any{
			map[string]any{"tag": "blocked", "protocol": "blackhole"},
			map[string]any{"tag": "tcp-echo", "protocol": "freedom", "settings": map[string]any{"redirect": tcpTarget.String()}},
			map[string]any{"tag": "udp-echo", "protocol": "freedom", "settings": map[string]any{"redirect": udpTarget.String()}},
		},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
			map[string]any{"type": "field", "domain": []string{"full:route.invalid"}, "network": "tcp", "outboundTag": "tcp-echo"},
			map[string]any{"type": "field", "domain": []string{"full:route.invalid"}, "network": "udp", "outboundTag": "udp-echo"},
		}},
	}
	encoded, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"xrayTemplateConfig": string(encoded), "subEnable": "false", "subJsonEnable": "false", "subClashEnable": "false"} {
		if err := (&SettingService{}).saveSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.CloseDB(); err != nil {
		t.Fatal(err)
	}
	return directory, password, httpPort, netip.MustParseAddrPort(productionSSHAddress(t)), apiAddress
}

func requireMieruPanelListenersReleased(t *testing.T, underlay string, native, api netip.AddrPort) {
	t.Helper()
	started := time.Now()
	deadline := started.Add(2 * time.Second)
	for _, endpoint := range []struct{ network, address string }{{"tcp", api.String()}, {underlay, native.String()}} {
		for {
			var closer io.Closer
			var err error
			if endpoint.network == "udp" {
				closer, err = net.ListenPacket("udp", endpoint.address)
			} else {
				closer, err = net.Listen("tcp", endpoint.address)
			}
			if err == nil {
				_ = closer.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("actual panel exit left %s/%s bound beyond two seconds: %v", endpoint.network, endpoint.address, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Logf("core and native listeners released after panel exit in %s", time.Since(started))
}

func (h *mieruPanelHTTP) waitMieru(t *testing.T, id int) {
	t.Helper()
	started := time.Now()
	deadline := started.Add(5 * time.Second)
	for {
		var statuses []MieruRuntimeStatus
		h.call(t, "/panel/api/inbounds/mieru/status", nil, &statuses)
		for _, status := range statuses {
			if status.InboundID == id && status.State == "running" {
				t.Logf("native runtime reported running after HTTP ready in %s", time.Since(started))
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("actual panel mieru listener did not recover within five seconds: %+v", statuses)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *mieruPanelHTTP) requireUsage(t *testing.T, email, up, down, billed string) ClientPolicy {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var policy ClientPolicy
		h.call(t, "/panel/api/clients/policy/"+email, nil, &policy)
		if policy.Supported && policy.Usage.Up == up && policy.Usage.Down == down && policy.Usage.Billed == billed && policy.Usage.Remainder == 0 {
			return policy
		}
		if time.Now().After(deadline) {
			t.Fatalf("public usage %s=%+v want=%s/%s/%s", email, policy.Usage, up, down, billed)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startMieruPanelProcess(t *testing.T, binaryPath, directory string, generation int) *mieruPanelProcess {
	t.Helper()
	log, err := os.OpenFile(filepath.Join(directory, fmt.Sprintf("panel-%d.log", generation)), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(binaryPath)
	cmd.Dir, cmd.Stdout, cmd.Stderr = directory, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &mieruPanelProcess{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Error("owned panel process group did not exit during cleanup")
		}
	})
	return p
}

func (p *mieruPanelProcess) stop(t *testing.T, signal syscall.Signal) {
	t.Helper()
	started := time.Now()
	if err := p.cmd.Process.Signal(signal); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("panel did not exit within the five-second shutdown bound")
	}
	if signal == syscall.SIGKILL {
		var exit *exec.ExitError
		if !errors.As(p.err, &exit) {
			t.Fatalf("panel did not report abnormal termination: %v", p.err)
		}
		status, ok := exit.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatalf("panel exited for a different reason: %v", exit)
		}
	} else if p.err != nil {
		t.Fatalf("panel graceful shutdown failed: %v", p.err)
	}
	t.Logf("panel exited for %s in %s", signal, time.Since(started))
}

type mieruPanelHTTP struct {
	client *http.Client
	origin string
}

func newMieruPanelHTTP(t *testing.T, port uint16, password string) *mieruPanelHTTP {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	h := &mieruPanelHTTP{client: &http.Client{Jar: jar, Transport: transport, Timeout: 10 * time.Second}, origin: fmt.Sprintf("http://127.0.0.1:%d", port)}
	deadline := time.Now().Add(15 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.origin+"/csrf-token", nil)
		response, err := h.client.Do(req)
		if err == nil {
			_ = response.Body.Close()
		}
		cancel()
		if err == nil && response.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("actual panel HTTP did not start within 15 seconds: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.call(t, "/login", map[string]string{"username": "mieru-process", "password": password}, nil)
	return h
}

func (h *mieruPanelHTTP) call(t *testing.T, path string, data, result any) {
	t.Helper()
	method := http.MethodGet
	var body io.Reader
	var token string
	if data != nil {
		h.call(t, "/csrf-token", nil, &token)
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		method, body = http.MethodPost, bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, h.origin+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.origin)
	if token != "" {
		req.Header.Set("X-CSRF-Token", token)
	}
	response, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("panel %s: %v", path, err)
	}
	defer response.Body.Close()
	var envelope struct {
		Success bool            `json:"success"`
		Message string          `json:"msg"`
		Object  json.RawMessage `json:"obj"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || response.StatusCode != http.StatusOK || !envelope.Success {
		t.Fatalf("panel %s rejected request: HTTP=%d success=%t message=%q decode=%v", path, response.StatusCode, envelope.Success, envelope.Message, err)
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Object, result); err != nil {
			t.Fatal(err)
		}
	}
}
