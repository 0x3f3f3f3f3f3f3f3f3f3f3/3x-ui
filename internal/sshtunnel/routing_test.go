package sshtunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/routedbridge"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func routingJSON(t *testing.T, v any) json_util.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func routedXray(t *testing.T, cfg *xray.Config, apiPort int) (*xray.XrayAPI, func()) {
	t.Helper()
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("unverified: set XRAY_E2E_BINARY to run the real SSH/Xray route acceptance")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, routingJSON(t, cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-config", path)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stop := sync.OnceFunc(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			t.Logf("Xray diagnostics: %s", output.String())
		}
	})
	tunnelWaitListener(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort)))
	api := &xray.XrayAPI{}
	if err := api.Init(apiPort); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)
	return api, stop
}

func routingExit(t *testing.T, marker, source string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				var payload [32]byte
				if _, err := io.ReadFull(conn, payload[:]); err == nil {
					observed, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
					if observed != source {
						_, _ = io.WriteString(conn, "bad-ip")
					} else {
						_, _ = io.WriteString(conn, marker)
					}
				}
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = l.Close(); wg.Wait() })
	return l.Addr().String()
}

func TestOpenSSHRoutesThroughXrayWithoutDuplicateBilling(t *testing.T) {
	if os.Getenv("XRAY_E2E_BINARY") == "" {
		t.Skip("unverified: set XRAY_E2E_BINARY to run the real SSH/Xray route acceptance")
	}
	db, ledger, controller, alice := tunnelDB(t)
	bob := model.ClientRecord{Email: "bob-route", Enable: true}
	if err := db.Create(&bob).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: bob.Email, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := controller.Configure(context.Background(), bob.PolicyID, policyflow.Rates{}); err != nil {
		t.Fatal(err)
	}
	bridgeAddr := netip.MustParseAddrPort(tunnelLocalAddress(t))
	bridge, err := routedbridge.New("ssh-routed", bridgeAddr, []routedbridge.ClientBinding{{PolicyID: alice.PolicyID, Email: alice.Email}, {PolicyID: bob.PolicyID, Email: bob.Email}})
	if err != nil {
		t.Fatal(err)
	}
	exitA, exitB := routingExit(t, "exit-a", "127.0.0.3"), routingExit(t, "exit-b", "127.0.0.4")
	apiAddr := netip.MustParseAddrPort(tunnelLocalAddress(t))
	nativeAddr := netip.MustParseAddrPort(tunnelLocalAddress(t))
	cfg := &xray.Config{
		LogConfig: routingJSON(t, map[string]any{"loglevel": "warning"}),
		API:       routingJSON(t, map[string]any{"tag": "api", "services": []string{"StatsService"}}),
		Stats:     json_util.RawMessage(`{}`),
		Policy:    json_util.RawMessage(`{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}},"system":{"statsInboundUplink":true,"statsInboundDownlink":true}}`),
		InboundConfigs: []xray.InboundConfig{
			{Tag: "api", Listen: json_util.RawMessage(`"127.0.0.1"`), Port: int(apiAddr.Port()), Protocol: "tunnel", Settings: json_util.RawMessage(`{"address":"127.0.0.1"}`)},
			{Tag: "native", Listen: json_util.RawMessage(`"127.0.0.1"`), Port: int(nativeAddr.Port()), Protocol: "socks", Settings: routingJSON(t, map[string]any{"auth": "password", "accounts": []any{map[string]any{"user": alice.Email, "pass": "native-only-test"}}})},
		},
		OutboundConfigs: routingJSON(t, []any{
			map[string]any{"tag": "blocked", "protocol": "blackhole"},
			map[string]any{"tag": "exit-a", "protocol": "freedom", "sendThrough": "127.0.0.3", "settings": map[string]any{"redirect": exitA}},
			map[string]any{"tag": "exit-b", "protocol": "freedom", "sendThrough": "127.0.0.4", "settings": map[string]any{"redirect": exitB}},
		}),
		RouterConfig: routingJSON(t, map[string]any{"domainStrategy": "AsIs", "rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
			map[string]any{"type": "field", "inboundTag": []string{"native"}, "outboundTag": "exit-a"},
			map[string]any{"type": "field", "domain": []string{"full:blocked.invalid"}, "outboundTag": "blocked"},
			map[string]any{"type": "field", "inboundTag": []string{"ssh-routed"}, "user": []string{alice.Email}, "source": []string{"127.0.0.2/32"}, "domain": []string{"full:route.invalid"}, "port": "443", "outboundTag": "exit-b"},
			map[string]any{"type": "field", "inboundTag": []string{"ssh-routed"}, "user": []string{alice.Email}, "source": []string{"127.0.0.1/32"}, "domain": []string{"full:route.invalid"}, "port": "443", "network": "tcp", "outboundTag": "exit-a"},
			map[string]any{"type": "field", "inboundTag": []string{"ssh-routed"}, "user": []string{"regexp:^bob-"}, "domain": []string{"full:route.invalid"}, "port": "443", "network": "tcp", "outboundTag": "exit-b"},
			map[string]any{"type": "field", "inboundTag": []string{"ssh-routed"}, "ip": []string{"192.0.2.7/32"}, "port": "8443", "outboundTag": "exit-b"},
			map[string]any{"type": "field", "inboundTag": []string{"ssh-routed"}, "user": []string{alice.Email}, "outboundTag": "exit-a"},
		}}),
	}
	if err := bridge.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	api, stopCore := routedXray(t, cfg, int(apiAddr.Port()))
	wrongEpoch, err := routedbridge.New("ssh-routed", bridgeAddr, []routedbridge.ClientBinding{{PolicyID: alice.PolicyID, Email: alice.Email}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := wrongEpoch.DialTCP(context.Background(), alice.PolicyID, netip.MustParseAddrPort("127.0.0.1:34567"), "route.invalid", 443)
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, routedbridge.ErrUnavailable) {
		t.Fatalf("bridge accepted credentials from a different owner epoch: %v", err)
	}
	if _, _, err := api.GetTraffic(); err != nil {
		t.Fatal(err)
	}
	native, _ := proxy.SOCKS5("tcp", nativeAddr.String(), &proxy.Auth{User: alice.Email, Password: "native-only-test"}, proxy.Direct)
	probe := func(dial func(string) (net.Conn, error), dest, want string) {
		t.Helper()
		conn, err := dial(dest)
		if err != nil {
			if want == "" {
				return
			}
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Write(bytes.Repeat([]byte("x"), 32))
		var reply [6]byte
		n, err := io.ReadFull(conn, reply[:])
		if want == "" {
			if n != 0 {
				t.Fatalf("blocked route reached an exit: %q, %v", reply[:n], err)
			}
			return
		}
		if err != nil || string(reply[:]) != want {
			t.Fatalf("%s: got %q, %v; want %s", dest, reply[:n], err, want)
		}
	}
	probe(func(dest string) (net.Conn, error) { return native.Dial("tcp", dest) }, "route.invalid:443", "exit-a")
	_, counters, err := api.GetTraffic()
	if err != nil || len(counters) != 1 || counters[0].Email != alice.Email || counters[0].Up != 32 || counters[0].Down != 6 {
		t.Fatalf("native Xray metering control: %+v, %v", counters, err)
	}
	host, _ := tunnelKey(t)
	keyA, keyPathA := tunnelKey(t)
	keyB, keyPathB := tunnelKey(t)
	server, err := NewServer(Config{InboundTag: "ssh-routed", HostKey: host, Clients: []Client{
		{PolicyID: alice.PolicyID, Username: "alice", PublicKeys: []ssh.PublicKey{keyA.PublicKey()}, Targets: []TargetRule{{Host: "*"}}},
		{PolicyID: bob.PolicyID, Username: "bob", PublicKeys: []ssh.PublicKey{keyB.PublicKey()}, Targets: []TargetRule{{Host: "*"}}},
	}}, controller, func(ctx context.Context, dest Destination) (io.ReadWriteCloser, error) {
		return bridge.DialTCP(ctx, dest.PolicyID, dest.Source, dest.Host, dest.Port)
	})
	if err != nil {
		t.Fatal(err)
	}
	address := tunnelServe(t, server)
	dialers := make(map[string]func(string) (net.Conn, error))
	for _, user := range []struct{ name, key string }{{"alice", keyPathA}, {"bob", keyPathB}} {
		local := tunnelLocalAddress(t)
		tunnelOpenSSHAs(t, user.name, address, user.key, host.PublicKey(), "-N", "-D", local)
		tunnelWaitListener(t, local)
		dialer, err := proxy.SOCKS5("tcp", local, nil, proxy.Direct)
		if err != nil {
			t.Fatal(err)
		}
		dialers[user.name] = func(dest string) (net.Conn, error) { return dialer.Dial("tcp", dest) }
	}
	probe(dialers["alice"], "route.invalid:443", "exit-a")
	probe(dialers["bob"], "route.invalid:443", "exit-b")
	probe(dialers["alice"], "192.0.2.7:8443", "exit-b")
	probe(dialers["alice"], "blocked.invalid:443", "")
	probe(dialers["bob"], "route.invalid:444", "")
	localSource := tunnelLocalAddress(t)
	tunnelOpenSSHAs(t, "alice", address, keyPathA, host.PublicKey(), "-b", "127.0.0.2", "-N", "-D", localSource)
	tunnelWaitListener(t, localSource)
	sourceDialer, err := proxy.SOCKS5("tcp", localSource, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	probe(func(dest string) (net.Conn, error) { return sourceDialer.Dial("tcp", dest) }, "route.invalid:443", "exit-b")
	_, counters, err = api.GetTraffic()
	if err != nil {
		t.Fatal(err)
	}
	for _, counter := range counters {
		if counter.Up != 0 || counter.Down != 0 {
			t.Fatalf("bridge repeated already-admitted client bytes in Xray: %+v", counter)
		}
	}
	account, err := ledger.Read(context.Background(), alice.PolicyID)
	if err != nil || account.Up != 128 || account.Down != 18 || account.Billed != 292 {
		t.Fatalf("routed Alice 2x accounting: %+v, %v", account, err)
	}
	t.Logf("actual OpenSSH→SOCKS→Xray exits, domain/IP/port/user/regexp/priority; Alice raw=%d/%d billed=%d; bridge Xray user deltas=0", account.Up, account.Down, account.Billed)
	stopCore()
	probe(dialers["alice"], "route.invalid:443", "")
	// A live direct target must remain unreachable when the routing backend is gone.
	probe(dialers["alice"], exitA, "")
}
