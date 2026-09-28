package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPortablePolicyRealSSH_Postgres(t *testing.T) {
	if os.Getenv("XUI_TEST_PG_DSN") == "" {
		t.Skip("set XUI_TEST_PG_DSN to an isolated PostgreSQL instance")
	}
	t.Setenv("XUI_DB_TYPE", "postgres")
	TestPortablePolicyRealSSHSurvivesRestoreAndRestart(t)
}

func TestPortablePolicyRealSSHSurvivesRestoreAndRestart(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY for actual OpenSSH portable restoration")
	}
	portableTestSchema(t)
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
	var accepted, targetUp, targetDown atomic.Int64
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer conn.Close()
				if n, err := conn.Write([]byte("x")); err == nil {
					targetDown.Add(int64(n))
				}
				var data [2]byte
				if n, err := io.ReadFull(conn, data[:]); err == nil {
					targetUp.Add(int64(n))
					if n, err := conn.Write(data[:]); err == nil {
						targetDown.Add(int64(n))
					}
				}
			}()
		}
	}()
	_, apiPortText, _ := net.SplitHostPort(productionSSHAddress(t))
	apiPort, _ := strconv.Atoi(apiPortText)
	template, _ := json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "warning"}, "stats": map[string]any{},
		"api":       map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"inbounds":  []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": apiPort, "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}},
		"outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "echo", "protocol": "freedom", "settings": map[string]any{"redirect": target.Addr().String()}}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}, map[string]any{"type": "field", "domain": []string{"full:route.invalid"}, "outboundTag": "echo"}}},
	})
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", string(template)); err != nil {
		t.Fatal(err)
	}
	xraySvc := &XrayService{}
	isManuallyStopped.Store(false)
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: xraySvc.GetXrayAPIPort, SetNeedRestart: xraySvc.SetToNeedRestart, SSHChanged: NotifySSHChange}))
	t.Cleanup(func() {
		_ = xraySvc.StopXray()
		runtime.SetManager(nil)
		isManuallyStopped.Store(false)
		isNeedXrayRestart.Store(false)
	})
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	keyBlock, _ := ssh.MarshalPrivateKey(key, "isolated portable test")
	keyPath := filepath.Join(t.TempDir(), "client-key")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	address := productionSSHAddress(t)
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	client := model.Client{Email: "portable-ssh", Enable: true, TotalGB: 1000, SSH: &model.SSHClient{PublicKeys: []string{string(ssh.MarshalAuthorizedKey(sshPub))}, Targets: []model.SSHTarget{{Host: "route.invalid", Port: 443}}}}
	settings, _ := json.Marshal(map[string]any{"clients": []model.Client{client}})
	inbound := &model.Inbound{Protocol: model.SSH, Enable: true, Listen: "127.0.0.1", Port: port, Settings: string(settings)}
	if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	var stored sshInboundSettings
	if err := json.Unmarshal([]byte(inbound.Settings), &stored); err != nil {
		t.Fatal(err)
	}
	host, err := ssh.ParsePrivateKey([]byte(stored.HostKey))
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("[127.0.0.1]:"+portText+" "+string(ssh.MarshalAuthorizedKey(host.PublicKey()))), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, ctx := &ClientService{}, context.Background()
	record := lookupClientRecord(t, client.Email)
	if _, err := svc.UpdatePolicy(ctx, client.Email, ClientPolicyUpdate{PolicyID: record.PolicyID, Scope: "local", Multiplier: "1.5", UploadBps: 65536, DownloadBps: 131072}); err != nil {
		t.Fatal(err)
	}
	if err := xraySvc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, address)
	portableSSHProbe(t, keyPath, knownHosts, portText, true)
	before, err := svc.GetPolicy(ctx, client.Email)
	if err != nil || before.Usage.Up != "2" || before.Usage.Down != "3" || before.Usage.Billed != "7" || before.Usage.Remainder != 500 {
		t.Fatalf("actual SSH starting history=%+v err=%v", before, err)
	}
	if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("total", 8).Error; err != nil {
		t.Fatal(err)
	}
	items := portableBrowserRoundTrip(t, svc)
	if _, err := svc.Delete(&InboundService{}, record.Id, true); err != nil {
		t.Fatal(err)
	}
	result, _, err := svc.ImportClients(&InboundService{}, items)
	if err != nil || result.Created != 1 || len(result.Skipped) != 0 {
		t.Fatalf("actual SSH restore=%+v err=%v", result, err)
	}
	for attempt := range 2 {
		if attempt > 0 {
			if err := xraySvc.StopXray(); err != nil {
				t.Fatal(err)
			}
		}
		if err := xraySvc.RestartXray(true); err != nil {
			t.Fatal(err)
		}
		productionSSHWait(t, address)
		portableSSHProbe(t, keyPath, knownHosts, portText, false)
		if accepted.Load() != 1 || targetUp.Load() != 2 || targetDown.Load() != 3 {
			t.Fatalf("exhausted restored identity reached target: accepted=%d raw=%d/%d", accepted.Load(), targetUp.Load(), targetDown.Load())
		}
		ledger := database.NewClientUsageLedger(database.GetDB())
		meter, err := ledger.ClaimAdmissionSource(ctx, lookupClientRecord(t, client.Email).PolicyID, "portable/denial-check")
		if err != nil {
			t.Fatal(err)
		}
		var quotaErr *database.UsageQuotaError
		if err := ledger.CheckAdmissionSource(ctx, meter.ID); !errors.As(err, &quotaErr) {
			t.Fatalf("restored admission failure is not exhausted quota: %v", err)
		}
	}
	if _, _, err := svc.BulkAdjust(&InboundService{}, []string{client.Email}, 0, 1000, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	portableSSHProbe(t, keyPath, knownHosts, portText, true)
	after, err := svc.GetPolicy(ctx, client.Email)
	if err != nil || after.PolicyID == record.PolicyID || after.UploadBps != 65536 || after.DownloadBps != 131072 || after.Multiplier != "1.5" || after.Usage.Up != "4" || after.Usage.Down != "6" || after.Usage.Billed != "15" || after.Usage.Remainder != 0 {
		t.Fatalf("actual SSH restored billing/history=%+v err=%v", after, err)
	}
	if targetUp.Load() != 4 || targetDown.Load() != 6 || accepted.Load() != 2 {
		t.Fatalf("independent target accounting mismatch: accepted=%d raw=%d/%d", accepted.Load(), targetUp.Load(), targetDown.Load())
	}
	t.Log("OpenSSH/Xray restore: 2/3 raw bytes -> 7.5 billed; quota 8 denied before/after restart; credit restored access and next 2/3 bytes brought billed usage to exactly 15")
}

func portableSSHProbe(t *testing.T, keyPath, knownHosts, port string, allowed bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-v", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+knownHosts, "-i", keyPath, "-p", port, "-W", "route.invalid:443", "portable-ssh@127.0.0.1")
	cmd.Stdin = strings.NewReader("ab")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if allowed {
		if err != nil || string(output) != "xab" {
			t.Fatalf("OpenSSH did not exchange routed payload: output=%q err=%v stderr=%s", output, err, stderr.String())
		}
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 255 || ctx.Err() != nil || len(output) != 0 || !strings.Contains(stderr.String(), "Authenticated to ") || !strings.Contains(stderr.String(), "closed by remote host") {
			t.Fatalf("restored quota did not close authenticated OpenSSH before forwarding: output=%q err=%v stderr=%s", output, err, stderr.String())
		}
	}
}
