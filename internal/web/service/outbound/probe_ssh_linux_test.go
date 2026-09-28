//go:build linux

package outbound

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/sshoutbound"
	"github.com/mhsanaei/3x-ui/v3/internal/testutil/sshdtest"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func realSSHProbe(t *testing.T) (map[string]any, *sshdtest.Endpoint, *[][]byte) {
	t.Helper()
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY for real outbound route probes")
	}
	upstream := sshdtest.Start(t)
	dir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", filepath.Join(dir, "logs"))
	if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	prior := newBatchProcess
	var configs [][]byte
	newBatchProcess = func(cfg *xray.Config, configPath string) batchProcess {
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		configs = append(configs, raw)
		return xray.NewTestProcess(cfg, configPath)
	}
	t.Cleanup(func() { newBatchProcess = prior })
	withEgressTraceProbe(t, func(*url.URL) *TestEgressResult { return nil })
	settings := sshoutbound.Config{Address: upstream.Address, Port: upstream.Port, User: upstream.User, PrivateKey: upstream.PrivateKey, HostKey: upstream.HostKey}
	return map[string]any{"tag": "ssh-probe", "protocol": "ssh", "settings": settings}, &upstream, &configs
}

func assertSSHProbeCleaned(t *testing.T, configs [][]byte) {
	t.Helper()
	for _, raw := range configs {
		if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "privateKey") {
			t.Fatal("temporary core configuration contains upstream private credentials")
		}
		var cfg struct {
			Inbounds []struct {
				Port int `json:"port"`
			} `json:"inbounds"`
			Outbounds []struct {
				Tag      string `json:"tag"`
				Protocol string `json:"protocol"`
				Settings struct {
					Address string `json:"address"`
					Port    int    `json:"port"`
				} `json:"settings"`
			} `json:"outbounds"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		var ownedPorts []int
		for _, inbound := range cfg.Inbounds {
			ownedPorts = append(ownedPorts, inbound.Port)
		}
		for _, ob := range cfg.Outbounds {
			if ob.Tag == "ssh-probe" && ob.Protocol == "socks" && ob.Settings.Address == "127.0.0.1" {
				ownedPorts = append(ownedPorts, ob.Settings.Port)
			}
		}
		for _, port := range ownedPorts {
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				t.Fatalf("probe retained its temporary SSH bridge: %v", err)
			}
			_ = listener.Close()
		}
	}
	files, err := filepath.Glob(filepath.Join(os.Getenv("XUI_BIN_FOLDER"), "*test*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary probe configuration was not removed: %v %v", files, err)
	}
}

func TestSSHProbeUsesRealCoreAndOpenSSH(t *testing.T) {
	ob, upstream, configs := realSSHProbe(t)
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	for _, tc := range []struct{ protocol, mode string }{{"ssh", "real"}, {"ssh", "http"}, {"ssh", "tcp"}, {"SSH", "real"}, {"Ssh", "tcp"}} {
		t.Run(tc.protocol+"/"+tc.mode, func(t *testing.T) {
			ob["protocol"] = tc.protocol
			mode := tc.mode
			before, authBefore := requests.Load(), upstream.AuthenticatedConnections(t)
			result, err := (&OutboundService{}).TestOutbound(mustJSON(t, ob), target.URL, "", mode)
			if err != nil || !result.Success || result.HTTPStatus != http.StatusNoContent {
				t.Fatalf("actual SSH HTTP route probe failed: result=%+v err=%v", result, err)
			}
			wantMode, wantRequests := "http", int64(2)
			if mode == "real" {
				wantMode, wantRequests = "real", 1
			}
			if result.Mode != wantMode || len(result.Endpoints) != 0 || requests.Load()-before != wantRequests {
				t.Fatalf("probe did not exercise the declared HTTP mode: %+v requests=%d", result, requests.Load()-before)
			}
			if got := upstream.AuthenticatedConnections(t) - authBefore; got != 1 {
				t.Fatalf("probe bypassed or reopened the upstream transport: authenticated=%d want=1", got)
			}
			assertSSHProbeCleaned(t, *configs)
		})
	}
}

func TestSSHProbeUsesRequestedPinAndIsolatesInvalidContext(t *testing.T) {
	ob, upstream, configs := realSSHProbe(t)
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	signer, err := ssh.ParsePrivateKey([]byte(upstream.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	wrong := ob["settings"].(sshoutbound.Config)
	wrong.HostKey = string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	wrongPin := map[string]any{"tag": "ssh-probe", "protocol": "ssh", "settings": wrong}
	t.Run("requested pin wins over stale context", func(t *testing.T) {
		before := requests.Load()
		result, err := (&OutboundService{}).TestOutbound(mustJSON(t, wrongPin), target.URL, mustJSON(t, []any{ob}), "real")
		if err != nil || result.Success || result.Error == "" || requests.Load() != before {
			t.Fatalf("stale context bypassed the requested host pin: %+v err=%v requests=%d", result, err, requests.Load()-before)
		}
		assertSSHProbeCleaned(t, *configs)
	})
	t.Run("malformed sibling does not poison valid probe", func(t *testing.T) {
		const secret = "DO-NOT-LOG-SSH-PROBE-PRIVATE-KEY"
		bad := map[string]any{"tag": "malformed-ssh", "protocol": "ssh", "settings": map[string]any{"privateKey": secret}}
		batch := mustJSON(t, []any{ob, bad})
		before, authBefore := requests.Load(), upstream.AuthenticatedConnections(t)
		results, err := (&OutboundService{}).TestOutbounds(batch, target.URL, batch, "real")
		if err != nil || len(results) != 2 || !results[0].Success || results[1].Success || results[1].Error == "" {
			t.Fatalf("invalid sibling poisoned the valid SSH route: results=%s err=%v", mustJSON(t, results), err)
		}
		if strings.Contains(mustJSON(t, results), secret) || requests.Load()-before != 1 || upstream.AuthenticatedConnections(t)-authBefore != 1 {
			t.Fatal("probe leaked credentials, bypassed SSH, or reached target for malformed credentials")
		}
		assertSSHProbeCleaned(t, *configs)
	})
}

func TestSSHProbeResolvesNativeProxyChain(t *testing.T) {
	ob, upstream, configs := realSSHProbe(t)
	ports, release, err := reserveLoopbackPorts(1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var nativeConfig xray.Config
	if err := json.Unmarshal([]byte(mustJSON(t, map[string]any{
		"inbounds":  []any{map[string]any{"tag": "native-socks", "listen": "127.0.0.1", "port": ports[0], "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{map[string]any{"protocol": "freedom"}},
	})), &nativeConfig); err != nil {
		t.Fatal(err)
	}
	native := xray.NewTestProcess(&nativeConfig, filepath.Join(os.Getenv("XUI_BIN_FOLDER"), "native-proxy.json"))
	release()
	if err := native.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = native.Stop() })
	if err := waitForPortsReady(native, ports, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	proxy := map[string]any{
		"tag": "native-proxy", "protocol": "socks", "settings": map[string]any{"address": "127.0.0.1", "port": ports[0]},
		"streamSettings": map[string]any{"sockopt": map[string]any{"dialerProxy": "middle"}},
	}
	middle := map[string]any{"tag": "middle", "protocol": "freedom", "streamSettings": map[string]any{"sockopt": map[string]any{"dialerProxy": "ssh-probe"}}}
	context := mustJSON(t, []any{ob, middle, map[string]any{"tag": "unused-malformed", "protocol": "ssh"}})
	result, err := (&OutboundService{}).TestOutbound(mustJSON(t, proxy), target.URL, context, "real")
	if err != nil || !result.Success || result.HTTPStatus != http.StatusNoContent || requests.Load() != 1 {
		t.Fatalf("native proxy chain via SSH failed: %+v err=%v requests=%d", result, err, requests.Load())
	}
	if got := upstream.AuthenticatedConnections(t); got != 1 {
		t.Fatalf("native proxy chain bypassed SSH: actual authenticated connections=%d", got)
	}
	assertSSHProbeCleaned(t, *configs)
}

func TestSSHProbeFailureCleansOwnedResources(t *testing.T) {
	ob, upstream, configs := realSSHProbe(t)
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	t.Run("missing core binary", func(t *testing.T) {
		path := filepath.Join(os.Getenv("XUI_BIN_FOLDER"), xray.GetBinaryName())
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Rename(path+".saved", path); err != nil {
				t.Error(err)
			}
		}()
		result, err := (&OutboundService{}).TestOutbound(mustJSON(t, ob), target.URL, "", "real")
		if err != nil || result.Success || result.Error == "" {
			t.Fatalf("missing binary was reported healthy: %+v err=%v", result, err)
		}
		assertSSHProbeCleaned(t, *configs)
	})
	t.Run("core rejects chain", func(t *testing.T) {
		broken := map[string]any{"tag": "broken-core", "protocol": "not-a-protocol", "streamSettings": map[string]any{"sockopt": map[string]any{"dialerProxy": "ssh-probe"}}}
		result, err := (&OutboundService{}).TestOutbound(mustJSON(t, broken), target.URL, mustJSON(t, []any{ob}), "real")
		if err != nil || result.Success || result.Error == "" {
			t.Fatalf("rejected core configuration was reported healthy: %+v err=%v", result, err)
		}
		assertSSHProbeCleaned(t, *configs)
	})
	t.Run("dead SSH upstream has no direct fallback", func(t *testing.T) {
		upstream.Stop()
		result, err := (&OutboundService{}).TestOutbound(mustJSON(t, ob), target.URL, "", "real")
		if err != nil || result.Success || result.Error == "" {
			t.Fatalf("dead upstream was reported healthy: %+v err=%v", result, err)
		}
		assertSSHProbeCleaned(t, *configs)
	})
	if requests.Load() != 0 || upstream.AuthenticatedConnections(t) != 0 {
		t.Fatal("failed probe reached the target or authenticated to an upstream")
	}
}
