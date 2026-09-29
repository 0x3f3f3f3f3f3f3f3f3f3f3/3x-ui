package mieru

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/pkg/stderror"
	statsservice "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mhsanaei/3x-ui/v3/internal/routedbridge"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestOfficialClientsThroughManagedCoreBillOnceAndRevoke(t *testing.T) {
	binaryPath := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for native clients through the managed core")
	}
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			first, a := mieruUser(t, db, ledger, controller, 1500)
			second, b := mieruUser(t, db, ledger, controller, 500)
			bridge, err := routedbridge.NewManaged("mieru-test", reserveManagedAddress(t), []routedbridge.ClientBinding{{PolicyID: a.PolicyID, Email: first.Email}, {PolicyID: b.PolicyID, Email: second.Email}})
			if err != nil {
				t.Fatal(err)
			}
			stats := startNativeManagedCore(t, binaryPath, bridge)
			server, err := New(Config{InboundTag: "mieru-test", Bindings: []Binding{{Network: underlay, Address: "127.0.0.1:0"}}, Clients: []Client{a, b}}, controller,
				func(ctx context.Context, d Destination) (net.Conn, error) {
					if d.Network == "udp" {
						return bridge.DialUDP(ctx, d.PolicyID, d.Source, d.Host, d.Port)
					}
					return bridge.DialTCP(ctx, d.PolicyID, d.Source, d.Host, d.Port)
				})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			var firstConns []net.Conn
			for i, user := range []Client{a, b} {
				client := officialClient(t, server.Addresses()[0], user)
				for _, network := range []string{"tcp", "udp"} {
					conn := wireEcho(t, client, nativeEcho(t, network), bytes.Repeat([]byte{byte(i + 11)}, 4096))
					if i == 0 {
						firstConns = append(firstConns, conn)
					}
				}
			}
			for _, want := range []struct {
				id     string
				billed int64
			}{{a.PolicyID, 24576}, {b.PolicyID, 8192}} {
				account, err := ledger.Read(t.Context(), want.id)
				if err != nil || account.Up != 8192 || account.Down != 8192 || account.Billed != want.billed {
					t.Fatalf("bridge duplicated billing or mixed users: %+v / %v; want billed %d", account, err, want.billed)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			userStats, err := stats.QueryStats(ctx, &statsservice.QueryStatsRequest{Pattern: "user>>>"})
			if err != nil || len(userStats.GetStat()) != 0 {
				t.Fatalf("core created a second per-user meter: %v / %v", userStats, err)
			}
			outStats, err := stats.QueryStats(ctx, &statsservice.QueryStatsRequest{Pattern: "outbound>>>native-direct>>>traffic"})
			if err != nil || len(outStats.GetStat()) != 2 {
				t.Fatalf("real outbound accounting missing: %v / %v", outStats, err)
			}
			for _, stat := range outStats.Stat {
				if stat.Value != 16384 {
					t.Fatalf("actual routed payload count = %d, want 16384: %s", stat.Value, stat.Name)
				}
			}
			if err := db.Model(&first).Update("enable", false).Error; err != nil {
				t.Fatal(err)
			}
			for _, conn := range firstConns {
				requireClosedWire(t, conn)
			}
			other := officialClient(t, server.Addresses()[0], b)
			otherTarget := nativeEcho(t, "udp")
			udpConn := wireEcho(t, other, otherTarget, []byte("unaffected"))
			tcpTarget := nativeEcho(t, "tcp")
			tcpConn, err := other.DialContext(t.Context(), tcpTarget)
			if err != nil {
				t.Fatal(err)
			}
			defer tcpConn.Close()
			if err := db.Model(&second).Update("total_gb", 8205).Error; err != nil {
				t.Fatal(err)
			}
			packet := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(udpConn))
			if _, err := packet.WriteTo([]byte("four"), otherTarget); err != nil {
				t.Fatal(err)
			}
			_ = udpConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			p := make([]byte, 64)
			n, _, err := packet.ReadFrom(p)
			var timeout net.Error
			if n != 0 || (!errors.Is(err, stderror.ErrTimeout) && (!errors.As(err, &timeout) || !timeout.Timeout())) {
				t.Fatalf("over-quota UDP reply was fragmented or closed the usable remainder: n=%d err=%v", n, err)
			}
			account, err := ledger.Read(t.Context(), b.PolicyID)
			if err != nil || account.Up != 8206 || account.Down != 8202 || account.Billed != 8204 {
				t.Fatalf("over-quota packet was charged: %+v / %v", account, err)
			}
			_ = udpConn.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := packet.WriteTo([]byte("x"), otherTarget); err != nil {
				t.Fatal(err)
			}
			if n, _, err := packet.ReadFrom(p); err != nil || string(p[:n]) != "x" {
				t.Fatalf("quota remainder was unusable: %q / %v", p[:n], err)
			}
			requireClosedWire(t, tcpConn)
			requireClosedWire(t, udpConn)
			requireDeniedSession(t, other, tcpTarget)
			account, err = ledger.Read(t.Context(), b.PolicyID)
			if err != nil || account.Up != 8207 || account.Down != 8203 || account.Billed != 8205 {
				t.Fatalf("final quota accounting duplicated or lost bytes: %+v / %v", account, err)
			}
		})
	}
}

func reserveManagedAddress(t *testing.T) netip.AddrPort {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := netip.MustParseAddrPort(l.Addr().String())
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func startNativeManagedCore(t *testing.T, binaryPath string, bridge *routedbridge.ManagedBridge) statsservice.StatsServiceClient {
	t.Helper()
	api := reserveManagedAddress(t)
	base := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"api":       map[string]any{"tag": "api", "listen": api.String(), "services": []string{"StatsService"}},
		"stats":     map[string]any{},
		"policy":    map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true}}, "system": map[string]any{"statsOutboundUplink": true, "statsOutboundDownlink": true}},
		"dns":       map[string]any{"hosts": map[string]any{"localhost": "127.0.0.1"}},
		"outbounds": []any{map[string]any{"tag": "native-direct", "protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.1"}}}}, "streamSettings": map[string]any{"sockopt": map[string]any{"domainStrategy": "UseIPv4"}}}},
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var cfg xray.Config
	if err := json.Unmarshal(encoded, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "core.json")
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "core.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binaryPath, "run", "-config", configPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
		if t.Failed() {
			output, _ := os.ReadFile(logPath)
			t.Logf("owned core output: %s", output)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := bridge.Check(t.Context()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed core failed authenticated readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := grpc.NewClient(api.String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return statsservice.NewStatsServiceClient(conn)
}
