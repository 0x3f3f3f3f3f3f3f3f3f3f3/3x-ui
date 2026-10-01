package sub

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func seedNativeSSHExport(t *testing.T) (*model.Inbound, *model.ClientRecord, model.NativeSSHHostKey) {
	t.Helper()
	cleanup, err := testpg.IsolatePackage("ssh_export")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	oldFS := distFS
	distFS = testDistFS
	t.Cleanup(func() { distFS = oldFS })
	seedSubDB(t)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	inbound, _, err := (&service.InboundService{}).AddInbound(&model.Inbound{UserId: 1, Protocol: model.SSH, Listen: "2001:db8::1", Port: 2222, Tag: "ssh-business", Remark: "原生 SSH", Settings: `{"clients":[],"allowPassword":true}`, StreamSettings: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(inbound).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	inbound.Enable = true
	client := &model.ClientRecord{Email: "label", SubID: "ssh-native-sub", Enable: true, SSHUsername: "独立%wire\\\"user", SSHAuthorizedKeys: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), SSHPassword: "never-export-this-ssh-password", Password: "never-export-other-protocol"}
	if err := database.GetDB().Create(client).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	var host model.NativeSSHHostKey
	if err := database.GetDB().First(&host, "id = ?", inbound.SSHHostKeyID).Error; err != nil {
		t.Fatal(err)
	}
	return inbound, client, host
}

func nativeSSHExportResponse(t *testing.T, format string) *httptest.ResponseRecorder {
	t.Helper()
	router := newSubscriptionTestRouter(subscriptionTestRouterConfig{})
	request := httptest.NewRequest(http.MethodGet, "/sub/ssh-native-sub?format="+format, nil)
	request.Header.Set("Accept", "text/html")
	if format == "raw" {
		request.Header.Set("Accept", "text/plain")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestSSHOpenSSHSubscriptionUsesPinnedPublicTrustAndSafeNativeConfig(t *testing.T) {
	inbound, client, host := seedNativeSSHExport(t)
	config := nativeSSHExportResponse(t, "ssh")
	if config.Code != http.StatusOK || !strings.Contains(config.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("OpenSSH configuration response: status=%d type=%s", config.Code, config.Header().Get("Content-Type"))
	}
	body := config.Body.String()
	for _, required := range []string{"StrictHostKeyChecking yes", "IdentitiesOnly yes", "UserKnownHostsFile", "IdentityFile", "2001:db8::1", "2222"} {
		if !strings.Contains(body, required) {
			t.Fatalf("native OpenSSH config omitted %s", required)
		}
	}
	for _, forbidden := range []string{client.SSHPassword, client.Password, host.PrivateKeyPEM, "PRIVATE KEY", "ProxyCommand", "sshpass", " -R "} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("OpenSSH configuration included inappropriate material: %s", forbidden)
		}
	}
	var alias string
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "Host" {
			alias = fields[1]
			break
		}
	}
	if alias == "" {
		t.Fatal("native configuration has no safe host alias")
	}
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal("required real OpenSSH client is unavailable")
	}
	path := filepath.Join(t.TempDir(), "business-ssh.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := exec.Command(sshBin, "-G", "-F", path, alias).CombinedOutput()
	if err != nil || !strings.Contains(string(parsed), "user "+client.SSHUsername+"\n") || !strings.Contains(string(parsed), "hostname 2001:db8::1\n") || !strings.Contains(string(parsed), "stricthostkeychecking true\n") {
		t.Fatalf("real OpenSSH parser did not retain native quoted identity/trust: %v", err)
	}
	known := nativeSSHExportResponse(t, "ssh-known-hosts")
	if known.Code != http.StatusOK {
		t.Fatalf("known_hosts response: %d", known.Code)
	}
	knownPath := filepath.Join(t.TempDir(), "business-known_hosts")
	if err := os.WriteFile(knownPath, known.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	callback, err := knownhosts.New(knownPath)
	if err != nil {
		t.Fatal(err)
	}
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(host.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort("2001:db8::1", fmt.Sprint(inbound.Port))
	if err := callback(address, &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: inbound.Port}, public); err != nil {
		t.Fatalf("native known_hosts did not pin SQL business trust: %v", err)
	}
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := ssh.NewSignerFromKey(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := callback(address, &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: inbound.Port}, otherSigner.PublicKey()); err == nil {
		t.Fatal("native known_hosts accepted a different business host key")
	}
	instructions := nativeSSHExportResponse(t, "ssh-instructions")
	if instructions.Code != http.StatusOK || !strings.Contains(instructions.Body.String(), " -D ") || !strings.Contains(instructions.Body.String(), " -L ") || strings.Contains(instructions.Body.String(), " -R ") || strings.Contains(instructions.Body.String(), client.SSHPassword) {
		t.Fatalf("SSH forwarding instructions are missing or incorrect: status=%d", instructions.Code)
	}
}

func TestSSHOpenSSHExportsRefuseUnsupportedFormatsAndInactiveBindings(t *testing.T) {
	inbound, client, _ := seedNativeSSHExport(t)
	base := NewSubService("")
	if _, _, err := NewSubJsonService("", "", "", "", base).GetJson(client.SubID, "panel.test", false); err == nil || !strings.Contains(strings.ToLower(err.Error()), "ssh") {
		t.Fatal("generic Xray JSON did not explain the OpenSSH export requirement")
	}
	if _, _, err := NewSubClashService(false, "", base).GetClash(client.SubID, "panel.test"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "ssh") {
		t.Fatal("generic Clash did not explain the OpenSSH export requirement")
	}
	for _, field := range []string{"exclude_from_sub", "enable"} {
		value := field == "exclude_from_sub"
		if err := database.GetDB().Model(inbound).Update(field, value).Error; err != nil {
			t.Fatal(err)
		}
		if response := nativeSSHExportResponse(t, "ssh"); response.Code != http.StatusNotFound {
			t.Fatalf("inactive/hidden SSH export %s: %d", field, response.Code)
		}
		if err := database.GetDB().Model(inbound).Update(field, !value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.GetDB().Model(client).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if response := nativeSSHExportResponse(t, "ssh"); response.Code != http.StatusNotFound {
		t.Fatalf("disabled SSH account export: %d", response.Code)
	}
}

func TestSSHOpenSSHExportNativeBoundaries(t *testing.T) {
	inbound, client, host := seedNativeSSHExport(t)
	for _, address := range []string{"127.0.0.1", "[2001:db8::1]", "例子.测试", "ssh.example.test"} {
		profile, err := service.SSHClientExport(inbound, client.Email, address, 2222, 1)
		if err != nil || profile == nil {
			t.Fatalf("native endpoint %q: %v", address, err)
		}
	}
	for _, address := range []string{"", "bad host", "bad\nProxyCommand touch", "-danger", "host%h.test", "host\".test"} {
		if _, err := service.SSHClientExport(inbound, client.Email, address, 2222, 1); err == nil {
			t.Fatalf("unsafe native endpoint accepted: %q", address)
		}
	}
	if err := database.GetDB().Create(&model.Host{InboundId: inbound.Id, Address: "ssh.example.test", Port: 2201}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.Host{InboundId: inbound.Id, Address: "例子.测试", Port: 2202}).Error; err != nil {
		t.Fatal(err)
	}
	response := nativeSSHExportResponse(t, "ssh")
	if response.Code != 200 || strings.Count(response.Body.String(), "\nHost ") != 2 || !strings.Contains(response.Body.String(), "xn--fsqu00a.xn--0zwm56d") {
		t.Fatalf("native endpoint fanout: %d %s", response.Code, response.Body.String())
	}
	if raw := nativeSSHExportResponse(t, "raw"); raw.Code != http.StatusNotAcceptable || !strings.Contains(raw.Body.String(), "format=ssh") {
		t.Fatalf("raw subscription did not explain native SSH files: %d", raw.Code)
	}
	if err := database.GetDB().Model(client).Update("expiry_time", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if response := nativeSSHExportResponse(t, "ssh"); response.Code != 404 {
		t.Fatalf("expired native SSH export: %d", response.Code)
	}
	if err := database.GetDB().Model(client).Update("expiry_time", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&model.Host{}).Where("inbound_id = ?", inbound.Id).Update("security", "tls").Error; err != nil {
		t.Fatal(err)
	}
	if response := nativeSSHExportResponse(t, "ssh"); response.Code != 500 {
		t.Fatalf("unsupported endpoint TLS exported: %d", response.Code)
	}
	if err := database.GetDB().Where("inbound_id = ?", inbound.Id).Delete(&model.Host{}).Error; err != nil {
		t.Fatal(err)
	}
	inbound.Settings = `{"allowPassword":true,"reverse":{"enabled":true,"bindAddresses":["::1"],"portFrom":2200,"portTo":2201,"sourceCIDRs":["::1/128"],"maxListeners":1}}`
	if err := database.GetDB().Model(inbound).Update("settings", inbound.Settings).Error; err != nil {
		t.Fatal(err)
	}
	if response := nativeSSHExportResponse(t, "ssh-instructions"); response.Code != 200 || !strings.Contains(response.Body.String(), " -R [::1]:2200:") || !strings.Contains(response.Body.String(), "server cannot verify that target") {
		t.Fatalf("authorized reverse instructions are missing or misleading: %d", response.Code)
	}
	if err := database.GetDB().Create(&model.Host{InboundId: inbound.Id, Address: "ssh.example.test", Path: "/unsupported-transport"}).Error; err != nil {
		t.Fatal(err)
	}
	if response := nativeSSHExportResponse(t, "ssh"); response.Code != 500 {
		t.Fatalf("native SSH silently discarded a host transport wrapper: %d", response.Code)
	}
	if err := database.GetDB().Where("inbound_id = ?", inbound.Id).Delete(&model.Host{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Delete(&host).Error; err != nil {
		t.Fatal(err)
	}
	if response := nativeSSHExportResponse(t, "ssh-known-hosts"); response.Code != 500 {
		t.Fatalf("missing SQL host trust silently exported: %d", response.Code)
	}
}
