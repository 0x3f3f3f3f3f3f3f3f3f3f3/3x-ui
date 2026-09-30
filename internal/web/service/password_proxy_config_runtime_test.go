package service

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	netproxy "golang.org/x/net/proxy"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func passwordConfigDial(mode string, port int, target, user, password string) (net.Conn, error) {
	endpoint := fmt.Sprintf("127.0.0.1:%d", port)
	if mode == "socks" {
		dialer, err := netproxy.SOCKS5("tcp", endpoint, &netproxy.Auth{User: user, Password: password}, &net.Dialer{Timeout: time.Second})
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return dialer.(netproxy.ContextDialer).DialContext(ctx, "tcp", target)
	}
	conn, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	if err == nil {
		var response *http.Response
		response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
		if err == nil && response.StatusCode != http.StatusOK {
			err = fmt.Errorf("proxy returned HTTP %d", response.StatusCode)
		}
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func TestPasswordProxyConfigFeedsRealSharedLedger(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built custom core")
	}
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	dir := filepath.Dir(policyConfigState(t).StateFile)
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	state, err := EnsureLocalClientPolicyState(dir)
	if err != nil {
		t.Fatal(err)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var accepts, received atomic.Int64
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			go func() {
				defer conn.Close()
				payload := make([]byte, 6)
				for {
					n, err := io.ReadFull(conn, payload)
					received.Add(int64(n))
					if err != nil {
						return
					}
					if _, err := conn.Write(payload); err != nil {
						return
					}
				}
			}()
		}
	}()
	first, second, disabled := passwordOwner(t, "live-password-first"), passwordOwner(t, "live-password-second"), passwordOwner(t, "live-password-disabled")
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: first.Email, Enable: true, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&disabled).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	var probes []net.Listener
	reserve := func() int {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = probe.Close() })
		probes = append(probes, probe)
		return probe.Addr().(*net.TCPAddr).Port
	}
	var listeners []*model.Inbound
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		accounts := []map[string]any{
			{"user": "alice", "pass": "resource-A", "ownerClientId": first.StableID},
			{"user": "ALICE", "pass": "resource-alias", "ownerClientId": first.StableID},
			{"user": "bob", "pass": "resource-B", "ownerClientId": second.StableID},
			{"user": "disabled", "pass": "resource-D", "ownerClientId": disabled.StableID},
		}
		ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: reserve(), Settings: passwordOwnerSettings(t, protocol, accounts...)})
		if err != nil {
			t.Fatal(err)
		}
		if err := database.GetDB().Model(ib).Update("enable", true).Error; err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, ib)
	}
	tunnel := mkInbound(t, reserve(), model.Tunnel, fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, target.Addr().(*net.TCPAddr).Port))
	if err := (&ClientService{}).SyncInbound(nil, tunnel.Id, []model.Client{*first.ToClient()}); err != nil {
		t.Fatal(err)
	}
	cfg, err := (&XrayService{}).GetManagedXrayConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	var generated conf.ClientPolicyConfig
	if err := json.Unmarshal(cfg.ClientPolicy, &generated); err != nil {
		t.Fatal(err)
	}
	for _, probe := range probes {
		_ = probe.Close()
	}
	process := xray.NewTestProcess(cfg, filepath.Join(dir, "password-config.json"))
	defer process.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	local := panelruntime.NewLocal(panelruntime.LocalDeps{})
	if err := local.StartManagedProcess(ctx, process, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
		return PrepareLocalClientPolicyBootstrap(caps, &generated)
	}); err != nil {
		t.Fatal(err)
	}
	previousProcess, _ := xrayState.snapshot()
	previousManager := panelruntime.GetManager()
	xrayState.replace(process)
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{}))
	defer func() {
		xrayState.replace(previousProcess)
		panelruntime.SetManager(previousManager)
	}()
	for _, transfer := range []struct {
		mode, user, password string
		listener             int
	}{{"socks", "alice", "resource-A", 0}, {"socks", "ALICE", "resource-alias", 0}, {"http", "alice", "resource-A", 0}, {"http", "alice", "resource-A", 1}, {"socks", "bob", "resource-B", 0}, {"http", "bob", "resource-B", 1}} {
		flow, err := passwordConfigDial(transfer.mode, listeners[transfer.listener].Port, target.Addr().String(), transfer.user, transfer.password)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = flow.Close() })
		managedActivationEcho(t, flow, "authed")
	}
	tunnelFlow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnelFlow.Close()
	managedActivationEcho(t, tunnelFlow, "tunnel")
	for _, proxy := range []struct {
		mode     string
		listener int
	}{{"socks", 0}, {"http", 0}, {"http", 1}} {
		for _, credential := range []struct{ user, password string }{{"alice", "wrong"}, {"unknown", "resource-A"}, {"disabled", "resource-D"}} {
			conn, err := passwordConfigDial(proxy.mode, listeners[proxy.listener].Port, target.Addr().String(), credential.user, credential.password)
			if conn != nil {
				_ = conn.Close()
			}
			if err == nil {
				t.Fatal("invalid or disabled password credential authenticated")
			}
		}
	}
	if received.Load() != 42 || accepts.Load() != 7 {
		t.Fatalf("independent target bytes/connections = %d/%d, want 42/7", received.Load(), accepts.Load())
	}
	if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, first.StableID); total.RawUpload != 130 || total.RawDownload != 230 || total.BilledBytes != 390 {
		t.Fatalf("aliases/Tunnel lost shared history or billing: %+v", total)
	}
	if total := policyLedgerTotal(t, second.StableID); total.RawUpload != 12 || total.RawDownload != 12 || total.BilledBytes != 36 {
		t.Fatalf("second owner attribution: %+v", total)
	}
	if total := policyLedgerTotal(t, disabled.StableID); total.RawUpload != 0 || total.RawDownload != 0 || total.BilledBytes != 0 {
		t.Fatalf("disabled owner received traffic: %+v", total)
	}
}
