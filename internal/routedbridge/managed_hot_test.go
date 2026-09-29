package routedbridge

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	statsservice "github.com/xtls/xray-core/app/stats/command"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestManagedBridgeHotAddPreservesCapabilityAndExistingTraffic(t *testing.T) {
	binaryPath := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for actual managed gRPC installation")
	}
	for _, level := range []uint32{4294967295, 255} {
		t.Run(strconv.FormatUint(uint64(level), 10), func(t *testing.T) {
			binding := ClientBinding{PolicyID: uuid.NewString(), Email: "hot-managed"}
			hot, err := NewManaged("hot-managed", netip.MustParseAddrPort(reserveDatagramCoreAddress(t)), []ClientBinding{binding})
			if err != nil {
				t.Fatal(err)
			}
			base, err := NewManaged("existing-managed", netip.MustParseAddrPort(reserveDatagramCoreAddress(t)), []ClientBinding{binding})
			if err != nil {
				t.Fatal(err)
			}
			apiAddress := netip.MustParseAddrPort(reserveDatagramCoreAddress(t))
			var hotJSON []byte
			startManagedCore(t, binaryPath, base, func(cfg map[string]any) {
				cfg["api"] = map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService"}}
				cfg["stats"] = map[string]any{}
				cfg["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}}}
				cfg["inbounds"] = []any{map[string]any{"tag": "api", "protocol": "tunnel", "listen": "127.0.0.1", "port": apiAddress.Port(), "settings": map[string]any{"address": "127.0.0.1"}}}
				cfg["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}}}
				encoded, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				var combined xray.Config
				if err := json.Unmarshal(encoded, &combined); err != nil {
					t.Fatal(err)
				}
				if err := hot.Apply(&combined); err != nil {
					t.Fatal(err)
				}
				inbound := combined.InboundConfigs[len(combined.InboundConfigs)-1]
				var settings map[string]any
				if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
					t.Fatal(err)
				}
				settings["clients"].([]any)[0].(map[string]any)["level"] = level
				inbound.Settings, err = json.Marshal(settings)
				if err != nil {
					t.Fatal(err)
				}
				hotJSON, err = json.Marshal(inbound)
				if err != nil {
					t.Fatal(err)
				}
				combined.InboundConfigs = combined.InboundConfigs[:len(combined.InboundConfigs)-1]
				encoded, err = json.Marshal(combined)
				if err != nil {
					t.Fatal(err)
				}
				clear(cfg)
				if err := json.Unmarshal(encoded, &cfg); err != nil {
					t.Fatal(err)
				}
			})
			api := &xray.XrayAPI{}
			if err := api.Init(int(apiAddress.Port())); err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			target, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			go func() {
				for {
					conn, err := target.Accept()
					if err != nil {
						return
					}
					go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
				}
			}()
			source := netip.MustParseAddrPort("127.0.0.2:12345")
			port := uint16(target.Addr().(*net.TCPAddr).Port)
			existing, err := base.DialTCP(t.Context(), binding.PolicyID, source, "localhost", port)
			if err != nil {
				t.Fatal(err)
			}
			defer existing.Close()
			echo := func(conn net.Conn, payload string) {
				t.Helper()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				if _, err := io.WriteString(conn, payload); err != nil {
					t.Fatal(err)
				}
				got := make([]byte, len(payload))
				if _, err := io.ReadFull(conn, got); err != nil || string(got) != payload {
					t.Fatalf("core stream payload=%q err=%v", got, err)
				}
			}
			echo(existing, "before hot installation")
			if err := api.AddInbound(hotJSON); err != nil {
				t.Fatalf("private generated bridge could not be hot installed: %v", err)
			}
			if err := hot.Check(t.Context()); err != nil {
				t.Fatalf("gRPC installation discarded managed capability: %v", err)
			}
			conn, err := hot.DialTCP(t.Context(), binding.PolicyID, source, "localhost", port)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			echo(conn, "hot TCP payload")
			echo(existing, "existing stream survives hot installation")
			udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer udp.Close()
			packets, err := hot.DialUDP(t.Context(), binding.PolicyID, source, "localhost", uint16(udp.LocalAddr().(*net.UDPAddr).Port))
			if err != nil {
				t.Fatal(err)
			}
			defer packets.Close()
			payload := bytes.Repeat([]byte{42}, 8193)
			_ = packets.SetDeadline(time.Now().Add(time.Second))
			if n, err := packets.Write(payload); n != len(payload) || err != nil {
				t.Fatalf("hot UDP upload %d, %v", n, err)
			}
			_ = udp.SetDeadline(time.Now().Add(time.Second))
			got := make([]byte, 65535)
			n, peer, err := udp.ReadFrom(got)
			if err != nil || !bytes.Equal(got[:n], payload) {
				t.Fatalf("hot UDP target received %d, %v", n, err)
			}
			if _, err := udp.WriteTo(payload, peer); err != nil {
				t.Fatal(err)
			}
			n, peer, err = packets.ReadFrom(got)
			if err != nil || !bytes.Equal(got[:n], payload) || peer.String() != udp.LocalAddr().String() {
				t.Fatalf("hot UDP reply lost bytes/actual peer: %d, %v, %v", n, peer, err)
			}
			stats, err := (*api.StatsServiceClient).QueryStats(t.Context(), &statsservice.QueryStatsRequest{Pattern: "user>>>hot-managed>>>"})
			if err != nil || len(stats.GetStat()) != 0 {
				t.Fatalf("hot bridge created duplicate client counters: %v, %v", stats, err)
			}
			if err := api.DelInbound("hot-managed"); err != nil {
				t.Fatal(err)
			}
			echo(existing, "existing stream survives hot removal")
		})
	}
}
