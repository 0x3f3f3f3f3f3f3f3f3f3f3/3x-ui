package job

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestSSHSourceCollectionDoesNotQueueHostWideIPBans(t *testing.T) {
	testManagedSourceDoesNotQueueHostWideIPBans(t, model.SSH)
}

func TestMieruSourceCollectionDoesNotQueueHostWideIPBans(t *testing.T) {
	testManagedSourceDoesNotQueueHostWideIPBans(t, model.Mieru)
}

func testManagedSourceDoesNotQueueHostWideIPBans(t *testing.T, protocol model.Protocol) {
	t.Helper()
	setupIntegrationDB(t)
	const email = "ssh-source-only"
	inbound := seedLinkedInboundWithClient(t, "ssh-ip-source", email, 1)
	if err := database.GetDB().Model(inbound).Update("protocol", protocol).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	seedClientIps(t, email, []IPWithTimestamp{{IP: "203.0.113.10", Timestamp: now - 1}})
	observed := map[string]map[string]int64{email: {"203.0.113.10": now - 1, "203.0.113.11": now}}
	NewCheckClientIpJob().processObserved(observed, true, true)
	if _, err := os.Stat(readIpLimitLogPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed source collection queued an unscoped host IP ban: stat error=%v", err)
	}
	if got := ipSet(readClientIps(t, email)); !reflect.DeepEqual(got, observed[email]) {
		t.Fatalf("collection lost a managed source: %v, want %v", got, observed[email])
	}
}

func TestSSHCollectorsObserveIdleClientsWithoutCoreOnlineAPI(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("unverified: set XRAY_E2E_BINARY for actual SSH online/IP collection")
	}
	setupIntegrationDB(t)
	t.Setenv("XUI_ENABLE_FAIL2BAN", "true")
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	if err := os.Symlink(binary, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	freePort := func() int {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().(*net.TCPAddr).Port
	}
	template := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"api":       map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"stats":     map[string]any{},
		"inbounds":  []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": freePort(), "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}},
		"outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}}},
	}
	encoded, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	if err := db.Where("key = ?", "xrayTemplateConfig").Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Setting{Key: "xrayTemplateConfig", Value: string(encoded)}).Error; err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	client := model.Client{Email: "ssh-idle-collector", Enable: true, SSH: &model.SSHClient{PublicKeys: []string{string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}}}
	settings, _ := json.Marshal(map[string]any{"clients": []model.Client{client}})
	inbound := &model.Inbound{Protocol: model.SSH, Enable: true, Listen: "127.0.0.1", Port: freePort(), Settings: string(settings)}
	var inboundSvc service.InboundService
	if _, _, err := inboundSvc.AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	var svc service.XrayService
	t.Cleanup(func() { _ = svc.StopXray() })
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		HostKey string `json:"hostKey"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &stored); err != nil {
		t.Fatal(err)
	}
	host, err := ssh.ParsePrivateKey([]byte(stored.HostKey))
	if err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(inbound.Port))
	var connection *ssh.Client
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, err = ssh.Dial("tcp", address, &ssh.ClientConfig{User: client.Email, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	deadline = time.Now().Add(2 * time.Second)
	for {
		users, _, err := inboundSvc.GetLocalSSHOnlineUsers()
		if err != nil {
			t.Fatal(err)
		}
		if len(users) == 1 && users[0].Email == client.Email {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real SSH session was not admitted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	process := service.XrayProcess()
	process.SetOnlineAPISupport(xray.OnlineAPIUnsupported)
	NewXrayTrafficJob().Run()
	if got := inboundSvc.GetOnlineClients(); !slices.Equal(got, []string{client.Email}) {
		t.Fatalf("idle SSH client missing from existing online list: %v", got)
	}
	if got := process.GetLocalActiveInbounds(); !slices.Contains(got, inbound.Tag) {
		t.Fatalf("idle SSH inbound missing from active attribution: %v", got)
	}
	var traffic xray.ClientTraffic
	if err := db.Where("email = ?", client.Email).First(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	if traffic.LastOnline < time.Now().UnixMilli()-2000 || traffic.Up != 0 || traffic.Down != 0 {
		t.Fatalf("idle SSH online observation must refresh last-online without inventing traffic: %+v", traffic)
	}
	ipJob := NewCheckClientIpJob()
	observed, ok := ipJob.collectFromOnlineAPI()
	if !ok || len(observed) != 1 || len(observed[client.Email]) != 1 || observed[client.Email]["127.0.0.1"] < time.Now().Unix()-2 {
		t.Fatalf("SSH actual peer missing without native online RPC: %+v, available=%v", observed, ok)
	}
	ipJob.Run()
	ips, err := inboundSvc.GetClientIpsWithNodes(client.Email)
	if err != nil || len(ips) != 1 || ips[0].IP != "127.0.0.1" || ips[0].Node != "" {
		t.Fatalf("existing local client IP view did not persist SSH source: %+v, %v", ips, err)
	}
	_ = connection.Close()
	deadline = time.Now().Add(2 * time.Second)
	for {
		observed, _ = ipJob.collectFromOnlineAPI()
		if len(observed) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnected SSH transport still contributes a live source: %+v", observed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	process.RefreshLocalOnline(nil, nil, time.Now().Add(21*time.Second).UnixMilli(), 20000)
	if got := inboundSvc.GetOnlineClients(); len(got) != 0 {
		t.Fatalf("disconnected SSH client survived existing online grace window: %v", got)
	}
}
