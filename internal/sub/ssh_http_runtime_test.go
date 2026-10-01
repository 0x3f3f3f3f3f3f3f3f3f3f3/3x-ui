package sub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	netproxy "golang.org/x/net/proxy"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/web/controller"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type sshHTTPHarness struct {
	router   *gin.Engine
	svc      *service.XrayService
	target   net.Listener
	template string
}

func sshHTTPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func newSSHHTTPHarness(t *testing.T) *sshHTTPHarness {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Fatal("real OpenSSH is required", err)
	}
	return newNativeHTTPHarness(t, "ssh_http")
}

func newNativeHTTPHarness(t *testing.T, namespace string) *sshHTTPHarness {
	t.Helper()
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built native core; required acceptance verifies PASS")
	}
	cleanup, err := testpg.IsolatePackage(namespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	seedSubDB(t)
	// Keep managed Unix control sockets below the platform's pathname limit.
	dir, err := os.MkdirTemp("", "native-http-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XUI_DB_FOLDER", dir)
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	apiPort := sshHTTPPort(t)
	template := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"api","services":["HandlerService","StatsService","RoutingService"]},"inbounds":[{"tag":"api","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"address":"127.0.0.1"}}],"outbounds":[{"protocol":"freedom","tag":"direct","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}],"routing":{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"}]},"stats":{},"policy":{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}}}}`, apiPort)
	if err := database.GetDB().Create(&model.Setting{Key: "xrayTemplateConfig", Value: template}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &service.XrayService{}
	restore := service.SetXrayProcessForTest(nil)
	previousManager := panelruntime.GetManager()
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{APIEndpoint: svc.GetXrayAPIEndpoint, SetNeedRestart: svc.SetToNeedRestart, ManagedChange: svc.ReconcileManagedChange}))
	service.StartTrafficWriter()
	t.Cleanup(func() {
		_ = svc.StopXray()
		service.StopTrafficWriter()
		restore()
		panelruntime.SetManager(previousManager)
		svc.IsNeedRestartAndSetFalse()
	})
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	router := gin.New()
	router.Use(func(c *gin.Context) { session.SetAPIAuthUser(c, &model.User{Id: 1}) })
	controller.NewInboundController(router.Group("/panel/api/inbounds"))
	controller.NewClientController(router.Group("/panel/api/clients"))
	controller.NewXraySettingController(router.Group("/panel"))
	NewSUBController(router.Group("/"), WithSUBEncryption(false))
	return &sshHTTPHarness{router: router, svc: svc, target: target, template: template}
}

func (h *sshHTTPHarness) api(t *testing.T, method, path string, payload any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	var envelope struct {
		Success bool            `json:"success"`
		Msg     string          `json:"msg"`
		Obj     json.RawMessage `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || w.Code != 200 || !envelope.Success {
		t.Fatalf("public API %s %s: %d %s %v", method, path, w.Code, w.Body.String(), err)
	}
	return envelope.Obj
}

func (h *sshHTTPHarness) add(t *testing.T, protocol, tag, settings string) model.Inbound {
	t.Helper()
	raw := h.api(t, http.MethodPost, "/panel/api/inbounds/add", map[string]any{"protocol": protocol, "tag": tag, "listen": "127.0.0.1", "port": sshHTTPPort(t), "enable": true, "settings": settings, "streamSettings": "{}"})
	var inbound model.Inbound
	if err := json.Unmarshal(raw, &inbound); err != nil {
		t.Fatal(err)
	}
	return inbound
}

func sshHTTPKey(t *testing.T) (string, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "ephemeral business acceptance key")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "business-user.key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), path
}

type sshHTTPProcess struct {
	done   chan struct{}
	err    error
	output bytes.Buffer
}

func (h *sshHTTPHarness) openSSH(t *testing.T, subID, privateFile string, options ...string) *sshHTTPProcess {
	t.Helper()
	dir := t.TempDir()
	var config, alias string
	for _, format := range []string{"ssh", "ssh-known-hosts"} {
		req := httptest.NewRequest(http.MethodGet, "/sub/"+subID+"?format="+format, nil)
		req.Host = "127.0.0.1"
		w := httptest.NewRecorder()
		h.router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("public OpenSSH export %s: %d", format, w.Code)
		}
		if format == "ssh" {
			config = w.Body.String()
		} else if err := os.WriteFile(filepath.Join(dir, "known_hosts"), w.Body.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, line := range strings.Split(config, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "Host" {
			alias = f[1]
			break
		}
	}
	if alias == "" {
		t.Fatal("missing OpenSSH host alias")
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	args := []string{"-F", filepath.Join(dir, "config"), "-o", "BatchMode=yes", "-o", "UserKnownHostsFile=" + filepath.Join(dir, "known_hosts"), "-i", privateFile, "-N"}
	args = append(args, options...)
	args = append(args, alias)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	p := &sshHTTPProcess{done: make(chan struct{})}
	cmd.Stderr = &p.output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { cancel(); <-p.done })
	return p
}

func sshHTTPFlow(t *testing.T, port int, p *sshHTTPProcess) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			t.Fatalf("real OpenSSH stopped: %v %s", p.err, p.output.String())
		default:
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			t.Cleanup(func() { _ = c.Close() })
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("real OpenSSH forward did not listen")
	return nil
}

func sshHTTPEcho(t *testing.T, c net.Conn, payload string) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c, payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(c, reply); err != nil || string(reply) != payload {
		t.Fatalf("actual native target reply: %q %v", reply, err)
	}
	_ = c.SetDeadline(time.Time{})
}

func sshHTTPClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err := c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("revoked flow remained open")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		t.Fatal("revoked flow did not close", err)
	}
}

func TestSSHHTTPExportRealOpenSSHAndSharedLifecycle(t *testing.T) {
	h := newSSHHTTPHarness(t)
	listener := h.add(t, "ssh", "native-ssh", `{"clients":[]}`)
	tunnel := h.add(t, "tunnel", "shared-tunnel", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, h.target.Addr().(*net.TCPAddr).Port))
	key, private := sshHTTPKey(t)
	siblingKey, siblingPrivate := sshHTTPKey(t)
	a := model.Client{Email: "http-ssh-owner", SubID: "http-ssh-owner-sub", Enable: true, TotalGB: 1000000, SSHUsername: "独立%wire\\\"user", SSHAuthorizedKeys: key, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	b := model.Client{Email: "http-ssh-sibling", SubID: "http-ssh-sibling-sub", Enable: true, SSHUsername: "sibling-wire", SSHAuthorizedKeys: siblingKey}
	h.api(t, http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: a, InboundIds: []int{listener.Id, tunnel.Id}})
	h.api(t, http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: b, InboundIds: []int{listener.Id}})
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	forward := func(subID, private string) net.Conn {
		t.Helper()
		port := sshHTTPPort(t)
		p := h.openSSH(t, subID, private, "-L", fmt.Sprintf("127.0.0.1:%d:%s", port, h.target.Addr()))
		return sshHTTPFlow(t, port, p)
	}
	first, other := forward(a.SubID, private), forward(b.SubID, siblingPrivate)
	dynamicPort := sshHTTPPort(t)
	dynamicProcess := h.openSSH(t, a.SubID, private, "-D", fmt.Sprintf("127.0.0.1:%d", dynamicPort))
	probe := sshHTTPFlow(t, dynamicPort, dynamicProcess)
	_ = probe.Close()
	dialer, err := netproxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", dynamicPort), nil, &net.Dialer{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dynamic, err := dialer.Dial("tcp", h.target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dynamic.Close() })
	shared, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	sshHTTPEcho(t, first, "local")
	sshHTTPEcho(t, dynamic, "socks")
	sshHTTPEcho(t, shared, "shared")
	sshHTTPEcho(t, other, "sibling")
	if _, _, err := h.svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	var owner model.ClientRecord
	if err := database.GetDB().Where("email = ?", a.Email).First(&owner).Error; err != nil {
		t.Fatal(err)
	}
	var total model.ClientPolicyTotal
	if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total.RawUpload != 16 || total.RawDownload != 16 || total.BilledBytes != 64 {
		t.Fatalf("public OpenSSH -L/-D/Tunnel exact shared usage: %+v", total)
	}
	rotated, rotatedPrivate := sshHTTPKey(t)
	a.SSHAuthorizedKeys = rotated
	h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
	sshHTTPClosed(t, first)
	sshHTTPClosed(t, dynamic)
	sshHTTPEcho(t, shared, "alive")
	sshHTTPEcho(t, other, "after-rotation")
	current := forward(a.SubID, rotatedPrivate)
	sshHTTPEcho(t, current, "rotated")
	stalePort := sshHTTPPort(t)
	stale := h.openSSH(t, a.SubID, private, "-L", fmt.Sprintf("127.0.0.1:%d:%s", stalePort, h.target.Addr()))
	select {
	case <-stale.done:
		if stale.err == nil {
			t.Fatal("revoked public key authenticated")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked public key was not refused")
	}
	detail := h.api(t, http.MethodGet, "/panel/api/clients/get/"+a.Email, nil)
	var view struct {
		Client model.ClientRecord `json:"client"`
	}
	if err := json.Unmarshal(detail, &view); err != nil {
		t.Fatal(err)
	}
	if view.Client.StableID != owner.StableID || view.Client.SSHAuthorizedKeys != rotated {
		t.Fatal("public read changed native owner/credentials")
	}
	exported := h.api(t, http.MethodGet, "/panel/api/clients/export", nil)
	var portable []service.ClientCreatePayload
	if err := json.Unmarshal(exported, &portable); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range portable {
		if item.Client.Email == a.Email {
			found = item.Client.SSHUsername == a.SSHUsername && item.Client.SSHAuthorizedKeys == rotated && len(item.InboundIds) == 2
		}
	}
	if !found {
		t.Fatal("portable export lost SSH authentication/membership")
	}
	h.api(t, http.MethodPost, "/panel/api/clients/import", map[string]any{"data": string(exported)})
	sshHTTPEcho(t, current, "reimport")
	for _, direction := range []string{"upload", "download"} {
		if direction == "upload" {
			a.Policy.UploadBytesPerSecond = 1
		} else {
			a.Policy.DownloadBytesPerSecond = 1
		}
		h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
		sshHTTPEcho(t, current, strings.Repeat("x", 65536))
		done := make(chan error, 1)
		go func() {
			_, err := io.WriteString(shared, "queued")
			if err == nil {
				reply := make([]byte, 6)
				_, err = io.ReadFull(shared, reply)
				if err == nil && string(reply) != "queued" {
					err = fmt.Errorf("queued response differs")
				}
			}
			done <- err
		}()
		select {
		case err := <-done:
			t.Fatalf("public %s policy did not share SSH/Tunnel rate bucket: %v", direction, err)
		case <-time.After(200 * time.Millisecond):
		}
		a.Policy.UploadBytesPerSecond = 0
		a.Policy.DownloadBytesPerSecond = 0
		h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("public live rate renewal did not release queued Tunnel")
		}
	}
	if _, _, err := h.svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total.RawUpload != 131120 || total.RawDownload != 131120 || total.BilledBytes != 524480 {
		t.Fatalf("public rate changes lost/repriced shared SSH history: %+v", total)
	}
	a.ExpiryTime = time.Now().Add(750 * time.Millisecond).UnixMilli()
	h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
	sshHTTPClosed(t, current)
	sshHTTPClosed(t, shared)
	sshHTTPEcho(t, other, "after-expiry")
	a.ExpiryTime = 0
	h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
	current = forward(a.SubID, rotatedPrivate)
	sshHTTPEcho(t, current, "renewed")
	a.Enable = false
	h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
	sshHTTPClosed(t, current)
	sshHTTPEcho(t, other, "after-disable")
	a.Enable = true
	h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
	current = forward(a.SubID, rotatedPrivate)
	sshHTTPEcho(t, current, "enabled")
	a.TotalGB = 1
	h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
	sshHTTPClosed(t, current)
	sshHTTPEcho(t, other, "after-quota")
	a.TotalGB = 1000000
	h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
	current = forward(a.SubID, rotatedPrivate)
	sshHTTPEcho(t, current, "quota-renewed")
	h.api(t, http.MethodPost, "/panel/api/clients/del/"+a.Email, nil)
	sshHTTPClosed(t, current)
	sshHTTPEcho(t, other, "after-delete")
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	reopened := forward(b.SubID, siblingPrivate)
	sshHTTPEcho(t, reopened, "restart")
	h.api(t, http.MethodPost, fmt.Sprintf("/panel/api/inbounds/del/%d", listener.Id), nil)
	sshHTTPClosed(t, reopened)
	released, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", listener.Port))
	if err != nil {
		t.Fatal("deleted SSH listener did not release port", err)
	}
	_ = released.Close()
}

func TestSSHHTTPAuthorizedReverseAndStrictNativeOutbound(t *testing.T) {
	h := newSSHHTTPHarness(t)
	reversePort := sshHTTPPort(t)
	listener := h.add(t, "ssh", "native-ssh", fmt.Sprintf(`{"clients":[],"reverse":{"enabled":true,"bindAddresses":["127.0.0.1"],"portFrom":%d,"portTo":%d,"sourceCIDRs":["127.0.0.1/32"],"maxListeners":2}}`, reversePort, reversePort))
	key, private := sshHTTPKey(t)
	a := model.Client{Email: "http-reverse", SubID: "http-reverse-sub", Enable: true, SSHUsername: "business", SSHAuthorizedKeys: key, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	h.api(t, http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: a, InboundIds: []int{listener.Id}})
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := h.openSSH(t, a.SubID, private, "-R", fmt.Sprintf("127.0.0.1:%d:%s", reversePort, h.target.Addr()))
	flow := sshHTTPFlow(t, reversePort, process)
	sshHTTPEcho(t, flow, "reverse")
	if _, _, err := h.svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	var owner model.ClientRecord
	if err := database.GetDB().Where("email = ?", a.Email).First(&owner).Error; err != nil {
		t.Fatal(err)
	}
	var total model.ClientPolicyTotal
	if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total.RawUpload != 7 || total.RawDownload != 7 || total.BilledBytes != 28 {
		t.Fatalf("public OpenSSH reverse did not retain shared identity: %+v", total)
	}
	deniedPort := sshHTTPPort(t)
	denied := h.openSSH(t, a.SubID, private, "-R", fmt.Sprintf("127.0.0.1:%d:%s", deniedPort, h.target.Addr()))
	select {
	case <-denied.done:
		if denied.err == nil {
			t.Fatal("reverse outside authorized range succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reverse outside authorized range not refused")
	}
	var host model.NativeSSHHostKey
	if err := database.GetDB().First(&host, "id = ?", listener.SSHHostKeyID).Error; err != nil {
		t.Fatal(err)
	}
	businessDir := filepath.Join(os.Getenv("XUI_DB_FOLDER"), "native-ssh", "outbound")
	if err := os.MkdirAll(businessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	material, err := os.ReadFile(private)
	if err != nil {
		t.Fatal(err)
	}
	outboundPrivate := filepath.Join(businessDir, "business-test.key")
	if err := os.WriteFile(outboundPrivate, material, 0o600); err != nil {
		t.Fatal(err)
	}
	var template map[string]any
	if err := json.Unmarshal([]byte(h.template), &template); err != nil {
		t.Fatal(err)
	}
	nativeOutbound := map[string]any{"protocol": "ssh", "tag": "native-outbound", "settings": map[string]any{"address": "127.0.0.1", "port": listener.Port, "username": a.SSHUsername, "privateKeyFile": outboundPrivate, "hostKey": host.PublicKey}}
	template["outbounds"] = append(template["outbounds"].([]any), nativeOutbound)
	template["routing"].(map[string]any)["rules"] = append(template["routing"].(map[string]any)["rules"].([]any), map[string]any{"type": "field", "inboundTag": []string{"native-outbound-probe"}, "outboundTag": "native-outbound"})
	save := func() {
		t.Helper()
		raw, err := json.Marshal(template)
		if err != nil {
			t.Fatal(err)
		}
		form := url.Values{"xraySetting": {string(raw)}}
		req := httptest.NewRequest(http.MethodPost, "/panel/xray/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.router.ServeHTTP(w, req)
		var result struct {
			Success bool `json:"success"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Success {
			t.Fatalf("public native outbound save: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	save()
	probe := h.add(t, "tunnel", "native-outbound-probe", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[{"email":"outbound-probe-owner","enable":true}]}`, h.target.Addr().(*net.TCPAddr).Port))
	connect := func() net.Conn {
		t.Helper()
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", probe.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	sshHTTPEcho(t, connect(), "strict-native-outbound")
	wrongKey, _ := sshHTTPKey(t)
	nativeOutbound["settings"].(map[string]any)["hostKey"] = wrongKey
	save()
	bad := connect()
	_, _ = io.WriteString(bad, "blocked")
	sshHTTPClosed(t, bad)
	nativeOutbound["settings"].(map[string]any)["hostKey"] = host.PublicKey
	save()
	sshHTTPEcho(t, connect(), "restored-pin")
}
