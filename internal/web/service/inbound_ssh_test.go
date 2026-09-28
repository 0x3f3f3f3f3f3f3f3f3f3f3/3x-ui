package service

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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestSSHInboundPreservesCanonicalCredentials(t *testing.T) {
	setupConflictDB(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	keyText := string(ssh.MarshalAuthorizedKey(key))
	settings, err := json.Marshal(map[string]any{"clients": []any{map[string]any{
		"id": uuid.NewString(), "email": "ssh-managed", "enable": true,
		"ssh": map[string]any{"publicKeys": []string{keyText}, "targets": []any{map[string]any{"host": "route.invalid", "port": 443}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{Protocol: model.Protocol("ssh"), Listen: "127.0.0.1", Port: 31280, Settings: string(settings)}
	if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	record := lookupClientRecord(t, "ssh-managed")
	encoded, err := json.Marshal(record.ToClient())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		SSH struct {
			PublicKeys []string `json:"publicKeys"`
			Targets    []struct {
				Host string `json:"host"`
				Port int    `json:"port"`
			} `json:"targets"`
		} `json:"ssh"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.SSH.PublicKeys) != 1 || got.SSH.PublicKeys[0] != keyText || len(got.SSH.Targets) != 1 || got.SSH.Targets[0].Host != "route.invalid" || got.SSH.Targets[0].Port != 443 {
		t.Fatal("the existing inbound creation path lost canonical SSH credentials or target permissions")
	}
	account, err := database.NewClientUsageLedger(database.GetDB()).Read(context.Background(), record.PolicyID)
	if err != nil || account.Revision != 1 {
		t.Fatalf("SSH creation did not claim accounting ownership before legacy jobs could mutate it: %v", err)
	}
	if _, err := (&ClientService{}).CreateOne(&InboundService{}, inbound.Id, model.Client{ID: uuid.NewString(), Email: "missing-ssh-key", Enable: true}); err == nil {
		t.Fatal("the existing client creation endpoint accepted SSH without credentials")
	}
	var invalid int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", "missing-ssh-key").Count(&invalid).Error; err != nil || invalid != 0 {
		t.Fatal("failed SSH client creation left a canonical record")
	}
	if _, err := (&ClientService{}).CreateOne(&InboundService{}, inbound.Id, model.Client{Email: "ssh-added", Enable: true, SSH: record.ToClient().SSH}); err != nil {
		t.Fatalf("existing client creation rejected a valid SSH identity without a proxy UUID: %v", err)
	}
	native := mkInbound(t, 31281, model.VLESS, clientsSettings(t, nil))
	if _, err := (&ClientService{}).Attach(&InboundService{}, record.Id, []int{native.Id}); err == nil {
		t.Fatal("an owned SSH account was attached to a path without policy enforcement")
	}
	var links int64
	if err := database.GetDB().Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", record.Id, native.Id).Count(&links).Error; err != nil || links != 0 {
		t.Fatal("rejected unmanaged attachment persisted a live binding")
	}
}

func productionSSHAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func productionSSHWait(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("managed listener did not become ready: %s", address)
}

func TestSSHInboundRunsThroughProductionXrayLifecycle(t *testing.T) {
	testSSHInboundProductionXrayLifecycle(t, nil)
}

func testSSHInboundProductionXrayLifecycle(t *testing.T, configure func(map[string]any)) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("unverified: set XRAY_E2E_BINARY for actual panel-managed Xray and OpenSSH")
	}
	setupConflictDB(t)
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	t.Setenv("XUI_LOG_FOLDER", filepath.Join(binDir, "logs"))
	if err := os.Symlink(binary, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	apiAddress := productionSSHAddress(t)
	_, apiPortText, _ := net.SplitHostPort(apiAddress)
	apiPort, _ := strconv.Atoi(apiPortText)
	template := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"api":       map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"stats":     map[string]any{},
		"inbounds":  []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": apiPort, "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}},
		"outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "echo", "protocol": "freedom", "settings": map[string]any{"redirect": echo.Addr().String()}}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}, map[string]any{"type": "field", "domain": []string{"full:route.invalid"}, "outboundTag": "echo"}}},
	}
	if configure != nil {
		configure(template)
	}
	encodedTemplate, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", string(encodedTemplate)); err != nil {
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
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	keyBlock, _ := ssh.MarshalPrivateKey(key, "isolated test client")
	keyPath := filepath.Join(t.TempDir(), "client-key")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	address := productionSSHAddress(t)
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	client := model.Client{Email: "ssh-panel", Enable: true, TotalGB: 4096, ExpiryTime: -int64(time.Hour / time.Millisecond), SSH: &model.SSHClient{PublicKeys: []string{string(ssh.MarshalAuthorizedKey(sshPub))}, Targets: []model.SSHTarget{{Host: "route.invalid", Port: 443}}}}
	otherPub, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSSHPub, _ := ssh.NewPublicKey(otherPub)
	otherClient := model.Client{Email: "ssh-unrelated", Enable: true, SSH: &model.SSHClient{PublicKeys: []string{string(ssh.MarshalAuthorizedKey(otherSSHPub))}, Targets: client.SSH.Targets}}
	settings, _ := json.Marshal(map[string]any{"clients": []model.Client{client, otherClient}})
	inbound := &model.Inbound{Protocol: model.SSH, Enable: true, Listen: "127.0.0.1", Port: port, Settings: string(settings)}
	if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = occupied.Close() })
	warning := fmt.Sprintf("managed SSH inbound %d protected: listener unavailable", inbound.Id)
	countWarnings := func() int {
		n := 0
		for _, line := range logger.GetLogs(10000, "warning") {
			if strings.Contains(line, warning) {
				n++
			}
		}
		return n
	}
	beforeWarnings := countWarnings()
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(650 * time.Millisecond)
	if got := countWarnings() - beforeWarnings; got != 1 {
		t.Fatalf("listener failure was silent or logged repeatedly: %d warnings", got)
	}
	productionSSHStatus(t, inbound.Id, "protected", 0, "listener unavailable")
	_ = occupied.Close()
	productionSSHWait(t, address)
	productionSSHStatus(t, inbound.Id, "running", 0, "")
	if lookupClientRecord(t, client.Email).ExpiryTime >= 0 {
		t.Fatal("starting the service consumed an unused client's delayed expiry")
	}
	cfgA, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfgB, err := svc.GetXrayConfig()
	if err != nil || !cfgA.Equals(cfgB) {
		t.Fatal("config reads rotate the private bridge credentials")
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
	local := productionSSHAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+knownHosts, "-o", "ExitOnForwardFailure=yes", "-i", keyPath, "-p", portText, "-N", "-L", local+":route.invalid:443", "ssh-panel@127.0.0.1")
	var output bytes.Buffer
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(output.String())
		}
	})
	productionSSHWait(t, local)
	conn, err := net.Dial("tcp", local)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	payload := []byte("actual panel SSH")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
		t.Fatalf("service-created SSH did not traverse real Xray: %v", err)
	}
	productionSSHStatus(t, inbound.Id, "running", 1, "")
	record := lookupClientRecord(t, client.Email)
	var projection xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", client.Email).First(&projection).Error; err != nil {
		t.Fatal(err)
	}
	settingsClient, _ := settingsClient(t, inbound.Id, client.Email)
	if record.ExpiryTime < time.Now().Add(59*time.Minute).UnixMilli() || projection.ExpiryTime != record.ExpiryTime || settingsClient.ExpiryTime != record.ExpiryTime {
		t.Fatal("authenticated first use did not persist one expiry across canonical, traffic and inbound settings")
	}
	account, err := database.NewClientUsageLedger(database.GetDB()).Read(context.Background(), record.PolicyID)
	if err != nil || account.Up != int64(len(payload)) || account.Down != int64(len(payload)) || account.Billed != int64(2*len(payload)) {
		t.Fatalf("production SSH lost or doubled payload accounting: %+v, %v", account, err)
	}
	info, err := os.Stat(xray.GetConfigPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private routing credentials have unsafe file permissions: %v", err)
	}

	otherSigner, _ := ssh.NewSignerFromKey(otherKey)
	unrelatedSSH := productionSSHDial(t, address, otherClient.Email, otherSigner, host.PublicKey())
	unrelated, err := unrelatedSSH.Dial("tcp", "route.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unrelated.Close() })
	productionSSHEcho(t, unrelated)
	productionSSHStatus(t, inbound.Id, "running", 2, "")
	// Model committed desired changes before their runtime notification arrives.
	// Reads must retain the observed sessions without applying those changes.
	if err := database.GetDB().Model(inbound).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	productionSSHStatus(t, inbound.Id, "pending", 2, "awaiting disable")
	productionSSHEcho(t, unrelated)
	if err := database.GetDB().Model(inbound).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	var pendingLinks []model.ClientInbound
	if err := database.GetDB().Where("inbound_id = ?", inbound.Id).Find(&pendingLinks).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Where("inbound_id = ?", inbound.Id).Delete(&model.ClientInbound{}).Error; err != nil {
		t.Fatal(err)
	}
	productionSSHStatus(t, inbound.Id, "pending", 2, "awaiting client update")
	productionSSHEcho(t, unrelated)
	if err := database.GetDB().Create(&pendingLinks).Error; err != nil {
		t.Fatal(err)
	}
	productionSSHStatus(t, inbound.Id, "running", 2, "")
	updated := *record.ToClient()
	updated.SSH = nil
	updated.Comment = "metadata must preserve credentials"
	if _, err := (&ClientService{}).UpdateByEmail(&InboundService{}, client.Email, updated, 0); err != nil {
		t.Fatal(err)
	}
	record = lookupClientRecord(t, client.Email)
	if record.ToClient().SSH == nil {
		t.Fatal("metadata update erased SSH credentials")
	}
	productionSSHEcho(t, conn)
	productionSSHEcho(t, unrelated)
	svc.ApplyPendingRestart()
	productionSSHEcho(t, unrelated)
	rotatedPublic, rotatedKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rotatedSSHPublic, _ := ssh.NewPublicKey(rotatedPublic)
	record = lookupClientRecord(t, client.Email)
	updated = *record.ToClient()
	updated.SSH.PublicKeys = []string{string(ssh.MarshalAuthorizedKey(rotatedSSHPublic))}
	if _, err := (&ClientService{}).UpdateByEmail(&InboundService{}, client.Email, updated, 0); err != nil {
		t.Fatal(err)
	}
	productionSSHClosed(t, conn)
	productionSSHEcho(t, unrelated)
	productionSSHStatus(t, inbound.Id, "running", 1, "")
	signer, _ := ssh.NewSignerFromKey(rotatedKey)
	live := productionSSHDial(t, address, client.Email, signer, host.PublicKey())
	flow, err := live.Dial("tcp", "route.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flow.Close() })
	productionSSHEcho(t, flow)
	if _, err := (&ClientService{}).ResetTrafficByEmail(&InboundService{}, client.Email); err != nil {
		t.Fatal(err)
	}
	productionSSHClosed(t, flow)
	productionSSHEcho(t, unrelated)
	// A fresh authentication must work after automatic source replacement, without a core restart.
	var recovered *ssh.Client
	var recoveredFlow net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recovered, err = ssh.Dial("tcp", address, &ssh.ClientConfig{User: client.Email, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
		if err == nil {
			recoveredFlow, err = recovered.Dial("tcp", "route.invalid:443")
		}
		if err == nil {
			break
		}
		if recovered != nil {
			_ = recovered.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("reset did not recover automatically: %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close(); _ = recoveredFlow.Close() })
	productionSSHEcho(t, recoveredFlow)
	resetAccount, err := database.NewClientUsageLedger(database.GetDB()).Read(context.Background(), record.PolicyID)
	if err != nil || resetAccount.Up != 1 || resetAccount.Down != 1 || resetAccount.Billed != 2 {
		t.Fatalf("post-reset usage reused the old boundary: %+v, %v", resetAccount, err)
	}
	record = lookupClientRecord(t, client.Email)
	updated = *record.ToClient()
	updated.TotalGB = 1
	if _, err := (&ClientService{}).UpdateByEmail(&InboundService{}, client.Email, updated, 0); err != nil {
		t.Fatal(err)
	}
	productionSSHClosed(t, recoveredFlow)
	productionSSHEcho(t, unrelated)
	updated.TotalGB = 4096
	if _, err := (&ClientService{}).UpdateByEmail(&InboundService{}, client.Email, updated, 0); err != nil {
		t.Fatal(err)
	}
	raised := productionSSHDial(t, address, client.Email, signer, host.PublicKey())
	raisedFlow, err := raised.Dial("tcp", "route.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raisedFlow.Close() })
	productionSSHEcho(t, raisedFlow)
	updated.Enable = false
	if _, err := (&ClientService{}).UpdateByEmail(&InboundService{}, client.Email, updated, 0); err != nil {
		t.Fatal(err)
	}
	productionSSHClosed(t, raisedFlow)
	svc.ApplyPendingRestart()
	productionSSHEcho(t, unrelated)
	updated.Enable = true
	if _, err := (&ClientService{}).UpdateByEmail(&InboundService{}, client.Email, updated, 0); err != nil {
		t.Fatal(err)
	}
	svc.ApplyPendingRestart()
	productionSSHEcho(t, unrelated)
	updated.Email = "ssh-renamed"
	if _, err := (&ClientService{}).UpdateByEmail(&InboundService{}, client.Email, updated, 0); err != nil {
		t.Fatal(err)
	}
	premature, err := ssh.Dial("tcp", address, &ssh.ClientConfig{User: updated.Email, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
	if err == nil {
		_ = premature.Close()
		t.Fatal("renamed SSH identity authenticated before its routed identity was applied")
	}
	svc.ApplyPendingRestart()
	productionSSHWait(t, address)
	client.Email = updated.Email
	renamed := productionSSHDial(t, address, client.Email, signer, host.PublicKey())
	renamedFlow, err := renamed.Dial("tcp", "route.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = renamedFlow.Close() })
	productionSSHEcho(t, renamedFlow)
	renamedRecord := lookupClientRecord(t, client.Email)
	if renamedRecord.PolicyID != record.PolicyID {
		t.Fatal("SSH rename replaced the billing owner")
	}
	fresh, err := (&InboundService{}).GetInbound(inbound.Id)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Enable = false
	if _, _, err := (&InboundService{}).UpdateInbound(fresh); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if probe, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		_ = probe.Close()
		t.Fatal("reconciliation resurrected a disabled SSH listener before the core config update")
	}
	productionSSHStatus(t, inbound.Id, "disabled", 0, "")
	fresh.Enable = true
	if _, _, err := (&InboundService{}).UpdateInbound(fresh); err != nil {
		t.Fatal(err)
	}
	productionSSHStatus(t, inbound.Id, "pending", 0, "awaiting configuration")
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, address)
	authenticated, err := ssh.Dial("tcp", address, &ssh.ClientConfig{User: client.Email, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer authenticated.Close()
	active, err := authenticated.Dial("tcp", "route.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if _, err := active.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(active, reply[:1]); err != nil {
		t.Fatal(err)
	}
	crashedAt := time.Now()
	if err := currentXrayProcess().Stop(); err != nil {
		t.Fatal(err)
	}
	productionSSHClosed(t, active)
	for {
		probe, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err != nil {
			break
		}
		_ = probe.Close()
		if time.Since(crashedAt) > 1250*time.Millisecond {
			t.Fatal("SSH listener remained open after the router process exited")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("router process exit protected the SSH listener in %s", time.Since(crashedAt))
	productionSSHStatus(t, inbound.Id, "protected", 0, "waiting for the applied Xray configuration")
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, address)
	authenticated = productionSSHDial(t, address, client.Email, signer, host.PublicKey())
	active, err = authenticated.Dial("tcp", "route.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	productionSSHEcho(t, active)
	nativeAddress := productionSSHAddress(t)
	_, nativePortText, _ := net.SplitHostPort(nativeAddress)
	nativePort, _ := strconv.Atoi(nativePortText)
	native := &model.Inbound{Protocol: model.VLESS, Enable: true, Listen: "127.0.0.1", Port: nativePort, Settings: `{"clients":[],"decryption":"none"}`}
	if _, _, err := (&InboundService{}).AddInbound(native); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, nativeAddress)
	native.Protocol, native.Settings = model.SSH, `{"clients":[]}`
	if _, _, err := (&InboundService{}).UpdateInbound(native); err != nil {
		t.Fatal(err)
	}
	if probe, err := net.DialTimeout("tcp", nativeAddress, 100*time.Millisecond); err == nil {
		_ = probe.Close()
		t.Fatal("native listener survived conversion to an empty protected SSH service")
	}
	productionSSHStatus(t, native.Id, "idle", 0, "no enabled clients")
	stoppedAt := time.Now()
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { var one [1]byte; _, err := active.Read(one[:]); stopped <- err }()
	select {
	case err := <-stopped:
		if err == nil || time.Since(stoppedAt) > 1250*time.Millisecond {
			t.Fatalf("managed SSH flow survived router shutdown beyond 1.25s: %v", err)
		}
	case <-time.After(1250*time.Millisecond - time.Since(stoppedAt)):
		t.Fatal("managed SSH flow survived router shutdown beyond 1.25s")
	}
	t.Logf("real service-created OpenSSH/Xray path billed %d payload bytes; router shutdown retired its flow in %s", account.Billed, time.Since(stoppedAt))
	productionSSHStatus(t, inbound.Id, "pending", 0, "awaiting configuration")
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, address)
	beforeRestore := productionSSHDial(t, address, client.Email, signer, host.PublicKey())
	beforeRestoreFlow, err := beforeRestore.Dial("tcp", "route.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer beforeRestoreFlow.Close()
	productionSSHEcho(t, beforeRestoreFlow)
	productionSSHStatus(t, inbound.Id, "running", 1, "")
	sshRuntimeState.Lock()
	previousManager := sshRuntimeState.manager
	sshRuntimeState.Unlock()
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	productionSSHStatus(t, inbound.Id, "pending", 0, "awaiting configuration")
	sshRuntimeState.Lock()
	readKeptManager := sshRuntimeState.manager == previousManager
	sshRuntimeState.Unlock()
	if !readKeptManager {
		t.Fatal("status read replaced the manager after database restoration")
	}
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, address)
	productionSSHStatus(t, inbound.Id, "running", 0, "")
	afterRestore := productionSSHDial(t, address, client.Email, signer, host.PublicKey())
	afterRestoreFlow, err := afterRestore.Dial("tcp", "route.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer afterRestoreFlow.Close()
	productionSSHEcho(t, afterRestoreFlow)
	productionSSHStatus(t, inbound.Id, "running", 1, "")
	if _, err := (&InboundService{}).DelInbound(inbound.Id); err != nil {
		t.Fatal(err)
	}
	productionSSHClosed(t, afterRestoreFlow)
	replacement := &model.Inbound{Protocol: model.SSH, Enable: true, Listen: "127.0.0.1", Port: port, Settings: `{"clients":[]}`}
	if _, _, err := (&InboundService{}).AddInbound(replacement); err != nil {
		t.Fatal(err)
	}
	productionSSHStatus(t, replacement.Id, "idle", 0, "no enabled clients")
	statuses, err := (&InboundService{}).GetSSHRuntimeStatuses(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.InboundID == inbound.Id {
			t.Fatal("deleted inbound still appears in runtime status after port reassignment")
		}
	}
}

func productionSSHDial(t *testing.T, address, user string, signer ssh.Signer, host ssh.PublicKey) *ssh.Client {
	t.Helper()
	raw, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.SetDeadline(time.Now().Add(2 * time.Second))
	conn, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host)})
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	_ = raw.SetDeadline(time.Time{})
	client := ssh.NewClient(conn, channels, requests)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func productionSSHEcho(t *testing.T, conn net.Conn) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte("x"))
		if err == nil {
			var one [1]byte
			_, err = io.ReadFull(conn, one[:])
			if err == nil && one[0] != 'x' {
				err = io.ErrUnexpectedEOF
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("existing SSH flow was disrupted: %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = conn.Close()
		t.Fatal("existing SSH flow stopped forwarding")
	}
}

func productionSSHClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	done := make(chan error, 1)
	go func() { var one [1]byte; _, err := conn.Read(one[:]); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked SSH flow still forwarded")
		}
	case <-time.After(1250 * time.Millisecond):
		_ = conn.Close()
		t.Fatal("SSH flow survived revocation for 1.25s")
	}
}

func TestSSHInbound_Postgres(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"credentials", TestSSHInboundPreservesCanonicalCredentials},
		{"real-data-path", TestSSHInboundRunsThroughProductionXrayLifecycle},
	} {
		t.Run(test.name, func(t *testing.T) { managedUsagePostgresSchema(t); test.run(t) })
	}
}

func TestSSHDetachedClientCredentialEdit(t *testing.T) {
	setupConflictDB(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ssh.NewPublicKey(public)
	client := model.Client{Email: "detached-ssh", Enable: true, SSH: &model.SSHClient{PublicKeys: []string{string(ssh.MarshalAuthorizedKey(key))}, Targets: []model.SSHTarget{{Host: "old.invalid", Port: 443}}}}
	record := client.ToRecord()
	if err := database.GetDB().Create(record).Error; err != nil {
		t.Fatal(err)
	}
	client.SSH.Targets[0].Host = "new.invalid"
	if _, err := (&ClientService{}).Update(&InboundService{}, record.Id, client, 0); err != nil {
		t.Fatal(err)
	}
	after := lookupClientRecord(t, client.Email)
	if after.ToClient().SSH.Targets[0].Host != "new.invalid" {
		t.Fatal("detached SSH client edit discarded credentials or permissions")
	}
	client.SSH = nil
	client.Comment = "metadata only"
	if _, err := (&ClientService{}).Update(&InboundService{}, record.Id, client, 0); err != nil {
		t.Fatal(err)
	}
	after = lookupClientRecord(t, client.Email)
	if after.ToClient().SSH.Targets[0].Host != "new.invalid" {
		t.Fatal("omitted SSH fields erased credentials")
	}
}

func TestSSHFirstUseDoesNotWaitForStalledWriter(t *testing.T) {
	setupConflictDB(t)
	client := model.ClientRecord{Email: "ssh-pending-auth", Enable: true, ExpiryTime: -60000}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	StartTrafficWriter()
	t.Cleanup(StopTrafficWriter)
	blocked, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		_ = submitTrafficWrite(func() error { close(blocked); <-release; return nil })
	}()
	<-blocked
	defer func() { close(release); <-finished }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- startManagedSSHClient(ctx, client.PolicyID) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancelled first-use mutation returned %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelled SSH authentication remained stuck behind the writer")
	}
}

func TestSSHPrepareFailureKeepsPriorAuthenticationState(t *testing.T) {
	setupConflictDB(t)
	t.Cleanup(stopManagedSSH)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ssh.NewPublicKey(public)
	client := model.Client{Email: "ssh-staged", Enable: true, SSH: &model.SSHClient{PublicKeys: []string{string(ssh.MarshalAuthorizedKey(key))}}}
	settings, _ := json.Marshal(map[string]any{"clients": []model.Client{client}})
	inbound := &model.Inbound{Protocol: model.SSH, Listen: "127.0.0.1", Port: 31290, Settings: string(settings)}
	if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	inbound.Enable = true
	cfg := &xray.Config{}
	plan, err := buildManagedSSH(cfg, []*model.Inbound{inbound})
	if err != nil {
		t.Fatal(err)
	}
	plan.apply(cfg)
	m := managedSSHRuntime()
	public, _, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nextKey, _ := ssh.NewPublicKey(public)
	client.SSH.PublicKeys = []string{string(ssh.MarshalAuthorizedKey(nextKey))}
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", client.Email).Update("ssh_config", client.ToRecord().SSHConfig).Error; err != nil {
		t.Fatal(err)
	}
	bad := &model.Inbound{Id: inbound.Id + 1, Protocol: model.SSH, Enable: true, Settings: "{"}
	if _, err := buildManagedSSH(&xray.Config{}, []*model.Inbound{inbound, bad}); err == nil {
		t.Fatal("invalid later entry was accepted")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !bytes.Equal(m.entries[inbound.Id].config.Clients[0].PublicKeys[0].Marshal(), key.Marshal()) {
		t.Fatal("failed config preparation partially replaced live authentication state")
	}
}
