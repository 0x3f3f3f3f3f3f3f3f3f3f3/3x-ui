package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type policyDuplex struct {
	up, down atomic.Int64
	accepted atomic.Int64
	ended    atomic.Int64
	target   net.Listener
	conns    []net.Conn
	wg       sync.WaitGroup
}

func policyDuplexTarget(t *testing.T) *policyDuplex {
	t.Helper()
	g := &policyDuplex{}
	var err error
	g.target, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.wg.Go(func() {
		for {
			conn, err := g.target.Accept()
			if err != nil {
				return
			}
			g.accepted.Add(1)
			g.wg.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				g.wg.Go(func() {
					buf := make([]byte, 32<<10)
					for {
						if _, err := conn.Write(buf); err != nil {
							return
						}
					}
				})
				buf := make([]byte, 32<<10)
				for {
					n, err := conn.Read(buf)
					g.up.Add(int64(n))
					if err != nil {
						return
					}
				}
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = g.target.Close(); g.close(); g.wg.Wait() })
	return g
}

func (g *policyDuplex) close() {
	for _, conn := range g.conns {
		_ = conn.Close()
	}
}

func (g *policyDuplex) start(t *testing.T, inbound *model.Inbound, email, keyPath, domain string) {
	t.Helper()
	var stored sshInboundSettings
	if err := json.Unmarshal([]byte(inbound.Settings), &stored); err != nil {
		t.Fatal(err)
	}
	host, err := ssh.ParsePrivateKey([]byte(stored.HostKey))
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	port := strconv.Itoa(inbound.Port)
	if err := os.WriteFile(knownHosts, []byte("[127.0.0.1]:"+port+" "+string(ssh.MarshalAuthorizedKey(host.PublicKey()))), 0o600); err != nil {
		t.Fatal(err)
	}
	local := productionSSHAddress(t)
	controlDir, err := os.MkdirTemp("", "xui-policy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(controlDir) })
	control := filepath.Join(controlDir, "master")
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+knownHosts, "-o", "ExitOnForwardFailure=yes", "-i", keyPath, "-p", port, "-M", "-S", control, "-N", "-L", local+":"+domain+":443", email+"@127.0.0.1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("OpenSSH %s: %s", email, stderr.String())
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		checkCtx, checkCancel := context.WithTimeout(ctx, 200*time.Millisecond)
		err := exec.CommandContext(checkCtx, "ssh", "-F", "/dev/null", "-S", control, "-O", "check", "127.0.0.1").Run()
		checkCancel()
		if err == nil {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("OpenSSH control socket did not become ready")
	}
	for range 2 {
		conn, err := net.Dial("tcp", local)
		if err != nil {
			t.Fatal(err)
		}
		g.conns = append(g.conns, conn)
		g.wg.Go(func() {
			defer g.ended.Add(1)
			buf := make([]byte, 32<<10)
			for {
				if _, err := conn.Write(buf); err != nil {
					return
				}
			}
		})
		g.wg.Go(func() {
			defer g.ended.Add(1)
			buf := make([]byte, 32<<10)
			for {
				n, err := conn.Read(buf)
				g.down.Add(int64(n))
				if err != nil {
					return
				}
			}
		})
	}
	deadline = time.Now().Add(5 * time.Second)
	for g.accepted.Load() < int64(len(g.conns)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if g.accepted.Load() != int64(len(g.conns)) {
		t.Fatal("not every OpenSSH channel reached the actual Xray-selected target")
	}
}

func TestClientPolicyProductionSSHSharedRatesChangeLive(t *testing.T) {
	testClientPolicyProductionSSHSharedRates(t, nil)
}

func testClientPolicyProductionSSHSharedRates(t *testing.T, configure func(map[string]any)) {
	testClientPolicyProductionSharedRates(t, configure, "")
}

func testClientPolicyProductionSharedRates(t *testing.T, configure func(map[string]any), nativeNetwork string) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY for real OpenSSH and panel-managed Xray")
	}
	setupConflictDB(t)
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	t.Setenv("XUI_LOG_FOLDER", filepath.Join(binDir, "logs"))
	if err := os.Symlink(binary, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	groups := []*policyDuplex{policyDuplexTarget(t), policyDuplexTarget(t)}
	domains := []string{"policy-a.invalid", "policy-b.invalid"}
	apiAddress := productionSSHAddress(t)
	_, apiPortText, _ := net.SplitHostPort(apiAddress)
	apiPort, _ := strconv.Atoi(apiPortText)
	template := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"api":       map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"stats":     map[string]any{},
		"inbounds":  []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": apiPort, "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}},
		"outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "a", "protocol": "freedom", "settings": map[string]any{"redirect": groups[0].target.Addr().String()}}, map[string]any{"tag": "b", "protocol": "freedom", "settings": map[string]any{"redirect": groups[1].target.Addr().String()}}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}, map[string]any{"type": "field", "domain": []string{"full:" + domains[0]}, "outboundTag": "a"}, map[string]any{"type": "field", "domain": []string{"full:" + domains[1]}, "outboundTag": "b"}}},
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
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: svc.GetXrayAPIPort, SetNeedRestart: svc.SetToNeedRestart, SSHChanged: NotifySSHChange, MieruChanged: NotifyMieruChange}))
	t.Cleanup(func() {
		_ = svc.StopXray()
		runtime.SetManager(nil)
		isManuallyStopped.Store(false)
		isNeedXrayRestart.Store(false)
	})
	clients := make([]model.Client, 2)
	keys := make([]string, 2)
	for n := range clients {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub, _ := ssh.NewPublicKey(pub)
		block, _ := ssh.MarshalPrivateKey(key, "isolated policy test")
		keys[n] = filepath.Join(t.TempDir(), "client-key")
		if err := os.WriteFile(keys[n], pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
		clients[n] = model.Client{Email: fmt.Sprintf("policy-%d", n), SubID: fmt.Sprintf("policy-sub-%d", n), Enable: true, SSH: &model.SSHClient{PublicKeys: []string{string(ssh.MarshalAuthorizedKey(sshPub))}, Targets: []model.SSHTarget{{Host: domains[n], Port: 443}}}}
		if nativeNetwork != "" {
			clients[n].Password = fmt.Sprintf("native-policy-password-%d", n)
		}
	}
	protocols := []model.Protocol{model.SSH, model.SSH}
	if nativeNetwork != "" {
		protocols = []model.Protocol{model.SSH, model.Mieru, model.Mieru}
	}
	inbounds := make([]*model.Inbound, len(protocols))
	for n := range inbounds {
		_, portText, _ := net.SplitHostPort(productionSSHAddress(t))
		port, _ := strconv.Atoi(portText)
		members := clients
		if n > 0 {
			members = nil
		}
		settings := map[string]any{"clients": members}
		if protocols[n] == model.Mieru {
			settings["network"] = nativeNetwork
		}
		encoded, _ := json.Marshal(settings)
		inbounds[n] = &model.Inbound{Protocol: protocols[n], Enable: true, Listen: "127.0.0.1", Port: port, Settings: string(encoded)}
		if _, _, err := (&InboundService{}).AddInbound(inbounds[n]); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			for _, client := range clients {
				record := lookupClientRecord(t, client.Email)
				if _, err := (&ClientService{}).Attach(&InboundService{}, record.Id, []int{inbounds[n].Id}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	policySvc, ctx := &ClientService{}, context.Background()
	policies := make([]ClientPolicy, 2)
	for n := range clients {
		var err error
		policies[n], err = policySvc.UpdatePolicy(ctx, clients[n].Email, ClientPolicyUpdate{PolicyID: lookupClientRecord(t, clients[n].Email).PolicyID, Multiplier: "2", Scope: "local"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("start policy Xray: %v; core result: %s", err, svc.GetXrayResult())
	}
	for _, inbound := range inbounds {
		if inbound.Protocol == model.Mieru {
			waitProductionMieru(t, inbound.Id)
		} else {
			productionSSHWait(t, sshListenAddress(inbound))
		}
	}
	for n := range clients {
		for _, inbound := range inbounds {
			if inbound.Protocol == model.Mieru {
				groups[n].startMieru(t, inbound, nativeNetwork, clients[n], domains[n])
			} else {
				groups[n].start(t, inbound, clients[n].Email, keys[n], domains[n])
			}
		}
	}
	time.Sleep(200 * time.Millisecond)
	before := [2]int64{groups[0].up.Load(), groups[0].down.Load()}
	start := time.Now()
	time.Sleep(400 * time.Millisecond)
	elapsed := time.Since(start).Seconds()
	for direction, count := range []int64{groups[0].up.Load() - before[0], groups[0].down.Load() - before[1]} {
		bps := float64(count) / elapsed
		if bps < 8*131072 {
			t.Fatalf("unlimited direction %d baseline %.0f B/s insufficient", direction, bps)
		}
		t.Logf("unlimited direction %d: %.0f B/s", direction, bps)
	}
	rates := [2][2]int64{{65536, 131072}, {131072, 65536}}
	apply := func(n int) {
		t.Helper()
		request := policies[n].ClientPolicyUpdate
		request.UploadBps, request.DownloadBps = rates[n][0], rates[n][1]
		var err error
		policies[n], err = policySvc.UpdatePolicy(ctx, clients[n].Email, request)
		if err != nil {
			t.Fatal(err)
		}
	}
	measure := func(label string) time.Time {
		t.Helper()
		before := [2][2]int64{}
		meterBefore := [2][2]int64{}
		meterStarted := [2]time.Time{}
		for n := range groups {
			meterStarted[n] = time.Now()
			policy, err := policySvc.GetPolicy(ctx, clients[n].Email)
			if err != nil {
				t.Fatal(err)
			}
			meterBefore[n][0], _ = strconv.ParseInt(policy.Usage.Up, 10, 64)
			meterBefore[n][1], _ = strconv.ParseInt(policy.Usage.Down, 10, 64)
		}
		start := time.Now()
		for n, g := range groups {
			before[n] = [2]int64{g.up.Load(), g.down.Load()}
		}
		time.Sleep(1500 * time.Millisecond)
		after := [2][2]int64{}
		for n, g := range groups {
			after[n] = [2]int64{g.up.Load(), g.down.Load()}
		}
		finished := time.Now()
		elapsed := finished.Sub(start).Seconds()
		for n, g := range groups {
			if ended := g.ended.Load(); ended != 0 {
				t.Fatalf("rate edit closed %d existing client %d streams", ended, n)
			}
			for direction, count := range []int64{after[n][0] - before[n][0], after[n][1] - before[n][1]} {
				rate := rates[n][direction]
				burst := min(int64(65536), max(int64(1), rate/10))
				lower, upper := float64(rate)*elapsed*0.80, float64(rate)*elapsed*1.06+float64(burst)
				if float64(count) < lower || float64(count) > upper {
					policy, err := policySvc.GetPolicy(ctx, clients[n].Email)
					up, _ := strconv.ParseInt(policy.Usage.Up, 10, 64)
					down, _ := strconv.ParseInt(policy.Usage.Down, 10, 64)
					t.Logf("usage diagnostic: %+v, %v; delivered=%d/%d; admitted in %.3fs=%d/%d", policy.Usage, err, after[n][0], after[n][1], time.Since(meterStarted[n]).Seconds(), up-meterBefore[n][0], down-meterBefore[n][1])
					t.Fatalf("%s client %d direction %d: %d bytes outside [%.0f,%.0f] in %.3fs", label, n, direction, count, lower, upper, elapsed)
				}
				t.Logf("%s client %d direction %d: %.0f raw B/s", label, n, direction, float64(count)/elapsed)
			}
		}
		return finished
	}
	for n := range clients {
		apply(n)
	}
	time.Sleep(400 * time.Millisecond)
	measure("initial")
	for _, next := range [][2]int64{{32768, 65536}, {131072, 131072}} {
		start := time.Now()
		rates[0] = next
		apply(0)
		appliedAfter := time.Since(start)
		time.Sleep(time.Until(start.Add(350 * time.Millisecond)))
		finished := measure("live")
		if elapsed := finished.Sub(start); elapsed > 2*time.Second {
			t.Fatalf("live policy edit exceeded 2s: %v", elapsed)
		}
		t.Logf("live policy write completed in %v; unchanged 1.5s rate window confirmed by %v", appliedAfter, finished.Sub(start))
	}
	for n := range clients {
		policy, err := policySvc.GetPolicy(ctx, clients[n].Email)
		if err != nil {
			t.Fatal(err)
		}
		up, _ := strconv.ParseInt(policy.Usage.Up, 10, 64)
		down, _ := strconv.ParseInt(policy.Usage.Down, 10, 64)
		billed, _ := strconv.ParseInt(policy.Usage.Billed, 10, 64)
		if up == 0 || down == 0 || billed != 2*(up+down) {
			t.Fatalf("2x billing diverged from raw rate accounting: %+v", policy.Usage)
		}
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		g.close()
		deadline := time.Now().Add(2 * time.Second)
		for g.ended.Load() < int64(2*len(g.conns)) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if g.ended.Load() != int64(2*len(g.conns)) {
			t.Fatal("managed shutdown left a live policy stream")
		}
		g.conns = nil
		g.ended.Store(0)
		g.accepted.Store(0)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	for _, inbound := range inbounds {
		if inbound.Protocol == model.Mieru {
			waitProductionMieru(t, inbound.Id)
		} else {
			productionSSHWait(t, sshListenAddress(inbound))
		}
	}
	for n := range clients {
		policy, err := policySvc.GetPolicy(ctx, clients[n].Email)
		if err != nil || policy.ClientPolicyUpdate != policies[n].ClientPolicyUpdate {
			t.Fatalf("restart lost the saved policy: %+v, %v", policy, err)
		}
		for _, inbound := range inbounds {
			if inbound.Protocol == model.Mieru {
				groups[n].startMieru(t, inbound, nativeNetwork, clients[n], domains[n])
			} else {
				groups[n].start(t, inbound, clients[n].Email, keys[n], domains[n])
			}
		}
	}
	time.Sleep(350 * time.Millisecond)
	measure("restart")
}

func TestClientPolicyProductionSSH_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestClientPolicyProductionSSHSharedRatesChangeLive(t)
}
