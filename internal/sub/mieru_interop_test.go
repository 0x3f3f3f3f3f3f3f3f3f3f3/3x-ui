package sub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	clientapi "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl"
	"github.com/enfein/mieru/v3/pkg/socks5"
	yaml "github.com/goccy/go-yaml"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestMieruSubscriptionConnectsOfficialClient(t *testing.T) {
	binary := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for official subscription interoperability")
	}
	for _, underlay := range []string{"tcp", "udp", "both"} {
		t.Run(underlay, func(t *testing.T) {
			initSubDB(t)
			binDir := t.TempDir()
			t.Setenv("XUI_BIN_FOLDER", binDir)
			t.Setenv("XUI_LOG_FOLDER", filepath.Join(binDir, "logs"))
			if err := os.Symlink(binary, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
				t.Fatal(err)
			}
			apiPort := mieruSubscriptionPort(t)
			template, err := json.Marshal(map[string]any{
				"log":       map[string]any{"loglevel": "warning"},
				"api":       map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
				"stats":     map[string]any{},
				"inbounds":  []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": apiPort, "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}},
				"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
				"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Create(&model.Setting{Key: "xrayTemplateConfig", Value: string(template)}).Error; err != nil {
				t.Fatal(err)
			}
			core := &service.XrayService{}
			runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: core.GetXrayAPIPort, SetNeedRestart: core.SetToNeedRestart, SSHChanged: service.NotifySSHChange, MieruChanged: service.NotifyMieruChange}))
			t.Cleanup(func() { _ = core.StopXray(); runtime.SetManager(nil) })
			settings, err := json.Marshal(map[string]any{"network": underlay, "clients": []model.Client{{Email: "native-subscription", Password: "fixture:p@ss/#?中文", Enable: true, SubID: "native-profile"}}})
			if err != nil {
				t.Fatal(err)
			}
			inbound := &model.Inbound{Protocol: model.Mieru, Enable: true, Listen: "127.0.0.1", Port: mieruSubscriptionPort(t), Settings: string(settings), ShareAddrStrategy: "custom", ShareAddr: "wrong.example"}
			if _, _, err := (&service.InboundService{}).AddInbound(inbound); err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Create(&model.Host{InboundId: inbound.Id, Address: "127.0.0.1", Port: inbound.Port, Remark: "native subscription", Security: "same"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := core.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				statuses, err := (&service.InboundService{}).GetMieruRuntimeStatuses(inbound.UserId)
				if err != nil {
					t.Fatal(err)
				}
				if len(statuses) == 1 && statuses[0].State == "running" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("native runtime not ready: %+v", statuses)
				}
				time.Sleep(10 * time.Millisecond)
			}
			staleSettings := strings.ReplaceAll(inbound.Settings, "fixture:p@ss/#?中文", "obsolete-password")
			if staleSettings == inbound.Settings {
				t.Fatal("stale credential fixture did not change settings")
			}
			if err := database.GetDB().Model(inbound).Update("settings", staleSettings).Error; err != nil {
				t.Fatal(err)
			}
			links, _, _, _, err := NewSubService("").GetSubs("native-profile", "panel.example")
			if err != nil || len(links) != 1 {
				t.Fatalf("subscription must export one native profile: count=%d err=%v", len(links), err)
			}
			profile, err := appctl.URLToClientProfile(links[0])
			if err != nil {
				t.Fatalf("official profile import: %v", err)
			}
			if profile.GetUser().GetPassword() != "fixture:p@ss/#?中文" {
				t.Fatal("subscription preferred stale settings over canonical credentials")
			}
			client := clientapi.NewClient()
			if err := client.Store(&clientapi.ClientConfig{Profile: profile}); err != nil {
				t.Fatal(err)
			}
			if err := client.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Stop() })
			for _, network := range []string{"tcp", "udp"} {
				target := mieruSubscriptionEcho(t, network)
				destination := apimodel.NetAddrSpec{Net: network, AddrSpec: apimodel.AddrSpec{IP: net.ParseIP("127.0.0.1"), Port: target}}
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				conn, err := client.DialContext(ctx, destination)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				payload := []byte("official subscription payload")
				response := make([]byte, len(payload))
				if network == "tcp" {
					if _, err := conn.Write(payload); err != nil {
						t.Fatal(err)
					}
					if _, err := io.ReadFull(conn, response); err != nil {
						t.Fatal(err)
					}
				} else {
					packets := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
					if _, err := packets.WriteTo(payload, destination); err != nil {
						t.Fatal(err)
					}
					n, peer, err := packets.ReadFrom(response)
					if err != nil || n != len(payload) || peer.String() != destination.String() {
						t.Fatalf("native subscription UDP echo: bytes=%d peer=%v err=%v", n, peer, err)
					}
				}
				if !bytes.Equal(payload, response) {
					t.Fatal("native subscription changed the payload")
				}
			}
			t.Run("mihomo", runMieruMihomoSubscription)
		})
	}
}

func runMieruMihomoSubscription(t *testing.T) {
	binary := os.Getenv("XUI_MIHOMO_E2E_BINARY")
	if binary == "" {
		t.Skip("set XUI_MIHOMO_E2E_BINARY for native Mihomo subscription interoperability")
	}
	svc := NewSubClashService(false, "", NewSubService(""))
	text, _, err := svc.GetClash("native-profile", "panel.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetClashLegacy("native-profile", "panel.example"); !errors.Is(err, errNoLegacyClashProxies) {
		t.Fatalf("legacy Clash must reject native-only subscription: %v", err)
	}
	var config map[string]any
	if err := yaml.Unmarshal([]byte(text), &config); err != nil {
		t.Fatal(err)
	}
	proxies, ok := config["proxies"].([]any)
	if !ok || len(proxies) == 0 {
		t.Fatal("Mihomo subscription has no native nodes")
	}
	for _, raw := range proxies {
		proxy := raw.(map[string]any)
		transport := proxy["transport"].(string)
		t.Run(transport, func(t *testing.T) {
			port := mieruSubscriptionPort(t)
			config["mixed-port"], config["bind-address"], config["allow-lan"] = port, "127.0.0.1", false
			config["log-level"] = "debug"
			config["proxy-groups"] = []map[string]any{{"name": "PROXY", "type": "select", "proxies": []string{proxy["name"].(string)}}}
			encoded, err := yaml.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if output, err := exec.CommandContext(ctx, binary, "-t", "-d", dir, "-f", path).CombinedOutput(); err != nil {
				t.Fatalf("official Mihomo validation failed: %v\n%s", err, output)
			}
			log, err := os.Create(filepath.Join(dir, "mihomo.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.CommandContext(ctx, binary, "-d", dir, "-f", path)
			cmd.Stdout, cmd.Stderr = log, log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() {
				cancel()
				<-done
				if t.Failed() {
					output, _ := os.ReadFile(filepath.Join(dir, "mihomo.log"))
					t.Logf("isolated Mihomo fixture log:\n%s", output)
				}
			}()
			address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			deadline := time.Now().Add(3 * time.Second)
			for {
				conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("official Mihomo SOCKS listener did not become ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			dialer := socks5.NewClientDialer(address, nil, true)
			dialer.Timeout = 2 * time.Second
			for _, network := range []string{"tcp", "udp"} {
				target := net.JoinHostPort("127.0.0.1", strconv.Itoa(mieruSubscriptionEcho(t, network)))
				payload := []byte("native Mihomo subscription payload")
				response := make([]byte, len(payload))
				if network == "tcp" {
					conn := readyMieruMihomoFlow(t, dialer, target)
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
					if _, err := conn.Write(payload); err != nil {
						t.Fatal(err)
					}
					if _, err := io.ReadFull(conn, response); err != nil {
						t.Fatal(err)
					}
				} else {
					conn, err := dialer.ListenPacket(ctx, "udp4", "127.0.0.1:0", target)
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
					peer, err := net.ResolveUDPAddr("udp4", target)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := conn.WriteTo(payload, peer); err != nil {
						t.Fatal(err)
					}
					n, from, err := conn.ReadFrom(response)
					if err != nil || n != len(payload) || from.String() != target {
						t.Fatalf("Mihomo UDP echo: bytes=%d peer=%v err=%v", n, from, err)
					}
				}
				if !bytes.Equal(payload, response) {
					t.Fatal("Mihomo subscription changed native payload")
				}
			}
		})
	}
}

func readyMieruMihomoFlow(t *testing.T, dialer *socks5.ClientDialer, target string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	for {
		conn, err := dialer.DialContext(ctx, "tcp", target)
		if err == nil {
			_ = conn.SetDeadline(deadline)
			_, err = conn.Write([]byte("ready"))
			if err == nil {
				var reply [5]byte
				_, err = io.ReadFull(conn, reply[:])
				if err == nil && string(reply[:]) == "ready" {
					return conn
				}
			}
			_ = conn.Close()
		}
		if ctx.Err() != nil {
			t.Fatalf("Mihomo native route did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mieruSubscriptionPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func mieruSubscriptionEcho(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			buffer := make([]byte, 65535)
			for {
				n, peer, err := conn.ReadFrom(buffer)
				if err != nil {
					return
				}
				_, _ = conn.WriteTo(buffer[:n], peer)
			}
		}()
		t.Cleanup(func() { _ = conn.Close(); <-done })
		return conn.LocalAddr().(*net.UDPAddr).Port
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				_, _ = io.Copy(conn, conn)
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	return listener.Addr().(*net.TCPAddr).Port
}
