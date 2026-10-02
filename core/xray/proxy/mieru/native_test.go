package mieru_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	miClient "github.com/enfein/mieru/v3/apis/client"
	miCommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/constant"
	"github.com/enfein/mieru/v3/apis/model"
	miServer "github.com/enfein/mieru/v3/apis/server"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	miError "github.com/enfein/mieru/v3/pkg/stderror"
	"github.com/xtls/xray-core/app/clientpolicy"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/infra/conf"
	_ "github.com/xtls/xray-core/main/distro/all"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/mieru"
	"github.com/xtls/xray-core/testing/testauthority"
	"google.golang.org/protobuf/proto"
)

const nativeClientID = "11111111-1111-4111-8111-111111111111"

func TestNativeMieruUDPRoutesEveryDestinationIncludingDomain(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			allowed := echoTarget(t, "udp").(*net.UDPAddr)
			denied, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer denied.Close()
			port := reservePort(t, mode)
			_ = nativeCoreConfigured(t, port, mode, func(config map[string]any) {
				config["outbounds"] = append(config["outbounds"].([]any), map[string]any{"protocol": "blackhole", "tag": "deny"})
				config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "port": fmt.Sprint(denied.LocalAddr().(*net.UDPAddr).Port), "outboundTag": "deny"}}}
			})
			client := referenceClient(t, port, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, allowed)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			packets := miCommon.NewUDPAssociateWrapper(miCommon.NewPacketOverStreamTunnel(conn))
			for _, target := range []net.Addr{allowed, model.NetAddrSpec{Net: "udp", AddrSpec: model.AddrSpec{FQDN: "localhost", Port: allowed.Port}}} {
				payload := []byte("per-destination-route")
				if _, err := packets.WriteTo(payload, target); err != nil {
					t.Fatal(err)
				}
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				result := make([]byte, 100)
				n, source, err := packets.ReadFrom(result)
				if err != nil || !bytes.Equal(result[:n], payload) || source.String() != allowed.String() {
					t.Fatalf("native UDP route/domain response %d %v %v", n, source, err)
				}
			}
			if _, err := packets.WriteTo([]byte("must-be-denied"), denied.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			_ = denied.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			if n, _, err := denied.ReadFrom(make([]byte, 100)); err == nil {
				t.Fatalf("UDP routing bypassed selected deny outbound: %d bytes", n)
			} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
				t.Fatalf("denied target observation failed: %v", err)
			}
		})
	}
}

func TestNativeMieruIPv6TCPDestination(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			port := reservePort(t, mode)
			_ = nativeCore(t, port, mode)
			client := referenceClient(t, port, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, listener.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			exchangeNative(t, conn, "native-ipv6-destination")
		})
	}
}

func TestNativeMieruLargeUDPDatagramKeepsBoundaryAndBilling(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			port := reservePort(t, mode)
			instance := nativeCore(t, port, mode)
			client := referenceClient(t, port, mode)
			target := echoTarget(t, "udp")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			packets := miCommon.NewUDPAssociateWrapper(miCommon.NewPacketOverStreamTunnel(conn))
			payload := bytes.Repeat([]byte("large-packet-"), 1000)
			if _, err := packets.WriteTo(payload, target); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			result := make([]byte, 65535)
			n, source, err := packets.ReadFrom(result)
			if err != nil || n != len(payload) || source.String() != target.String() || !bytes.Equal(result[:n], payload) {
				t.Fatalf("one UDP packet truncated or split: %d want %d source %v err %v", n, len(payload), source, err)
			}
			manager := instance.GetFeature((*clientpolicy.Manager)(nil)).(clientpolicy.Manager)
			snapshot, err := manager.Snapshot(nativeClientID)
			want := clientpolicy.Usage{RawUpload: uint64(len(payload)), RawDownload: uint64(len(payload)), BilledBytes: uint64(len(payload) * 3)}
			if err != nil || snapshot.Usage != want {
				t.Fatalf("large decrypted packet ledger %v want %v err %v", snapshot.Usage, want, err)
			}
		})
	}
}

func TestNativeMieruConcurrentMuxSameCredentialsRemainUsable(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			port := reservePort(t, mode)
			_ = nativeCore(t, port, mode)
			client := referenceClient(t, port, mode)
			target := echoTarget(t, "tcp")
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			for i := 0; i < 12; i++ {
				conn, err := client.DialContext(ctx, target)
				if err != nil {
					t.Fatalf("independent stream %d on authenticated mux: %v", i, err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				exchangeNative(t, conn, fmt.Sprintf("multiplex-stream-%d", i))
			}
		})
	}
}

func TestNativeMieruSharesTunnelPolicyAndLiveQuota(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			target := echoTarget(t, "tcp").(*net.TCPAddr)
			port, tunnelPort := reservePort(t, mode), reservePort(t, "TCP")
			instance := nativeCoreConfigured(t, port, mode, func(config map[string]any) {
				config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "shared-tunnel", "listen": "127.0.0.1", "port": tunnelPort, "protocol": "dokodemo-door", "settings": map[string]any{"network": "tcp", "address": "127.0.0.1", "port": target.Port, "clientId": nativeClientID, "email": "canonical-alice", "allowedSourceCidrs": []string{"127.0.0.0/8"}}})
			})
			client := referenceClient(t, port, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			tunnel, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort))
			if err != nil {
				t.Fatal(err)
			}
			defer tunnel.Close()
			exchangeNative(t, conn, "mieru-first")
			exchangeNative(t, tunnel, "tunnel-first")
			manager := instance.GetFeature((*clientpolicy.Manager)(nil)).(clientpolicy.Manager)
			policy := clientpolicy.Policy{ClientID: nativeClientID, Version: 2, Enabled: true, Multiplier: 2000000, BurstBytes: 65536}
			if err := testauthority.Apply(t, manager.(*clientpolicy.Engine), policy); err != nil {
				t.Fatal(err)
			}
			exchangeNative(t, conn, "mieru-second")
			exchangeNative(t, tunnel, "tunnel-second")
			before := uint64(len("mieru-first") + len("tunnel-first"))
			after := uint64(len("mieru-second") + len("tunnel-second"))
			want := clientpolicy.Usage{RawUpload: before + after, RawDownload: before + after, BilledBytes: before*3 + after*4}
			snapshot, err := manager.Snapshot(nativeClientID)
			if err != nil || snapshot.Usage != want {
				t.Fatalf("shared Tunnel ledger: %v want %v err %v", snapshot.Usage, want, err)
			}
			policy.Version, policy.QuotaBytes = 3, want.BilledBytes
			if err := testauthority.Apply(t, manager.(*clientpolicy.Engine), policy); err != nil {
				t.Fatal(err)
			}
			for _, active := range []net.Conn{conn, tunnel} {
				_ = active.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := active.Read(make([]byte, 1)); err == nil {
					t.Fatal("live shared quota left an active path")
				} else if nativeIsTimeout(err) {
					t.Fatal("quota closure merely timed out")
				}
			}
			final, err := manager.Snapshot(nativeClientID)
			if err != nil || final.Usage != want || final.Reasons&clientpolicy.ReasonQuota == 0 {
				t.Fatalf("quota enforcement repriced or lost historical traffic: %v/%v", final, err)
			}
		})
	}
}

func TestNativeMieruUnauthenticatedTimeoutReleasesCapacity(t *testing.T) {
	port := reservePort(t, "TCP")
	_ = nativeCoreConfigured(t, port, "TCP", func(config map[string]any) {
		settings := config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
		settings["handshakeTimeoutSeconds"], settings["maxConnections"] = 1, 1
	})
	unauthenticated, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer unauthenticated.Close()
	_ = unauthenticated.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := unauthenticated.Read(make([]byte, 1)); err == nil {
		t.Fatal("unexpected plaintext response")
	} else if nativeIsTimeout(err) {
		t.Fatal("unauthenticated socket retained past handshake limit")
	}
	client := referenceClient(t, port, "TCP")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, echoTarget(t, "tcp"))
	if err != nil {
		t.Fatalf("expired unauthenticated socket retained capacity: %v", err)
	}
	defer conn.Close()
	exchangeNative(t, conn, "after-timeout")
}

func TestNativeMieruCloseReleasesNativeListeners(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			port := reservePort(t, mode)
			instance := nativeCore(t, port, mode)
			if err := instance.Close(); err != nil {
				t.Fatal(err)
			}
			address := fmt.Sprintf("127.0.0.1:%d", port)
			if mode == "TCP" {
				listener, err := net.Listen("tcp", address)
				if err != nil {
					t.Fatal(err)
				}
				_ = listener.Close()
			} else {
				conn, err := net.ListenPacket("udp", address)
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.Close()
			}
		})
	}
}

func exchangeNative(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	result := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, result); err != nil || string(result) != payload {
		t.Fatalf("payload exchange %q: %v", result, err)
	}
}

func TestNativeMieruCredentialRotationClosesOldMuxAndPreservesSibling(t *testing.T) {
	for _, transport := range []string{"TCP", "UDP"} {
		t.Run(transport, func(t *testing.T) {
			port := reservePort(t, transport)
			instance := nativeCoreConfigured(t, port, transport, func(config map[string]any) {
				settings := config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
				settings["users"] = append(settings["users"].([]any), map[string]any{"username": "bob", "password": "bob-secret", "email": "canonical-bob", "clientId": "bob-id"})
			})
			target := echoTarget(t, "tcp")
			alice := referenceClient(t, port, transport)
			bob := referenceClientCredentials(t, port, transport, "bob", "bob-secret")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			a, err := alice.DialContext(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			b, err := bob.DialContext(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			exchangeNative(t, a, "alice-before")
			exchangeNative(t, b, "bob-before")
			handler, err := instance.GetFeature(inbound.ManagerType()).(inbound.Manager).GetHandler(ctx, "native-mieru")
			if err != nil {
				t.Fatal(err)
			}
			users := handler.(proxy.GetInbound).GetInbound().(proxy.UserManager)
			if err := users.RemoveUser(ctx, "canonical-alice"); err != nil {
				t.Fatal(err)
			}
			_ = a.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := a.Read(make([]byte, 1)); err == nil {
				t.Fatal("removed credential remains live")
			} else if nativeIsTimeout(err) {
				t.Fatal("removed credential merely timed out")
			}
			exchangeNative(t, b, "bob-after-removal")
			user := &protocol.MemoryUser{ClientID: nativeClientID, Email: "canonical-alice", Account: &mieru.Account{Username: "alice", Password: "rotated-business-secret"}}
			if err := users.AddUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
			defer probeCancel()
			stop := context.AfterFunc(probeCtx, func() { _ = alice.Stop() })
			stale, staleErr := alice.DialContext(probeCtx, target)
			stop()
			if staleErr == nil {
				_ = stale.Close()
				t.Fatal("cached old password authenticated after rotation")
			}
			fresh := referenceClientCredentials(t, port, transport, "alice", "rotated-business-secret")
			current, err := fresh.DialContext(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			exchangeNative(t, current, "alice-new-password")
			exchangeNative(t, b, "bob-after-rotation")
		})
	}
}

func reservePort(t *testing.T, transport string) int {
	t.Helper()
	if transport == "UDP" {
		conn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := conn.LocalAddr().(*net.UDPAddr).Port
		_ = conn.Close()
		return port
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func referenceServer(t *testing.T, port int, transport string) {
	t.Helper()
	mode := appctlpb.TransportProtocol_TCP
	if transport == "UDP" {
		mode = appctlpb.TransportProtocol_UDP
	}
	server := miServer.NewServer()
	if err := server.Store(&miServer.ServerConfig{Config: &appctlpb.ServerConfig{
		ListenIPAddress: proto.String("127.0.0.1"),
		PortBindings:    []*appctlpb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: mode.Enum()}},
		Users:           []*appctlpb.User{{Name: proto.String("alice"), Password: proto.String("native-business-secret")}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	go func() {
		for {
			conn, request, err := server.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if request.Command == constant.Socks5ConnectCmd {
					target, err := net.DialTimeout("tcp", request.DstAddr.String(), 3*time.Second)
					if err != nil {
						return
					}
					defer target.Close()
					if err := model.WriteSocks5Response(conn, constant.Socks5ReplySuccess, model.AddrSpec{IP: net.IPv4zero}); err != nil {
						return
					}
					go func() { _, _ = io.Copy(target, conn) }()
					_, _ = io.Copy(conn, target)
				} else {
					if err := model.WriteSocks5Response(conn, constant.Socks5ReplySuccess, model.AddrSpec{IP: net.IPv4zero}); err != nil {
						return
					}
					packets := miCommon.NewUDPAssociateWrapper(miCommon.NewPacketOverStreamTunnel(conn))
					payload := make([]byte, 65535)
					for {
						n, destination, err := packets.ReadFrom(payload)
						if err != nil {
							return
						}
						target, err := net.DialTimeout("udp", destination.String(), 3*time.Second)
						if err != nil {
							return
						}
						_ = target.SetDeadline(time.Now().Add(3 * time.Second))
						_, err = target.Write(payload[:n])
						if err == nil {
							n, err = target.Read(payload)
						}
						_ = target.Close()
						if err != nil {
							return
						}
						if _, err := packets.WriteTo(payload[:n], destination); err != nil {
							return
						}
					}
				}
			}()
		}
	}()
}

func TestNativeMieruOutboundUsesOfficialServer(t *testing.T) {
	for _, transport := range []string{"TCP", "UDP"} {
		for _, network := range []string{"tcp", "udp"} {
			t.Run(transport+"/"+network, func(t *testing.T) {
				port := reservePort(t, transport)
				referenceServer(t, port, transport)
				raw := fmt.Sprintf(`{"outbounds":[{"protocol":"mieru","settings":{"address":"127.0.0.1","port":%d,"transport":%q,"username":"alice","password":"native-business-secret","multiplexing":"MULTIPLEXING_OFF"}}]}`, port, transport)
				var config conf.Config
				if err := json.Unmarshal([]byte(raw), &config); err != nil {
					t.Fatal(err)
				}
				built, err := config.Build()
				if err != nil {
					t.Fatal(err)
				}
				instance, err := core.New(built)
				if err != nil {
					t.Fatalf("native outbound cannot construct core: %v", err)
				}
				t.Cleanup(func() { _ = instance.Close() })
				if err := instance.Start(); err != nil {
					t.Fatal(err)
				}
				target := echoTarget(t, network)
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				destination := xnet.DestinationFromAddr(target)
				conn, err := core.Dial(ctx, instance, destination)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				payload := bytes.Repeat([]byte("outbound-data!"), 100)
				if _, err := conn.Write(payload); err != nil {
					t.Fatal(err)
				}
				result := make([]byte, len(payload))
				if _, err := io.ReadFull(conn, result); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(result, payload) {
					t.Fatal("official server payload was corrupted")
				}
			})
		}
	}
}

func nativeCore(t *testing.T, port int, transport string) *core.Instance {
	return nativeCoreConfigured(t, port, transport, nil)
}

func nativeCoreConfigured(t *testing.T, port int, transport string, configure func(map[string]any)) *core.Instance {
	t.Helper()
	stateFile := filepath.Join(t.TempDir(), "policy.db")
	if err := clientpolicy.CreateStore(stateFile, "native-mieru-test"); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"clientPolicy": map[string]any{
			"stateFile": stateFile, "instanceId": "native-mieru-test",
			"policies": []clientpolicy.Policy{{ClientID: nativeClientID, Version: 1, Enabled: true, Multiplier: 1500000, BurstBytes: 65536}, {ClientID: "bob-id", Version: 1, Enabled: true, Multiplier: 1000000, BurstBytes: 65536}},
		},
		"inbounds": []any{map[string]any{
			"tag": "native-mieru", "listen": "127.0.0.1", "port": port, "protocol": "mieru",
			"settings": map[string]any{"transport": transport, "users": []any{map[string]any{"username": "alice", "password": "native-business-secret", "email": "canonical-alice", "clientId": nativeClientID}}},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	if configure != nil {
		configure(config)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var configJSON conf.Config
	if err := json.Unmarshal(raw, &configJSON); err != nil {
		t.Fatal(err)
	}
	built, err := configJSON.Build()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.New(built)
	if err != nil {
		t.Fatalf("native mieru cannot construct a runnable core: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Start(); err != nil {
		t.Fatalf("native mieru listener cannot start: %v", err)
	}
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	for _, policy := range configJSON.ClientPolicy.Policies {
		testauthority.Grant(t, engine, policy)
	}
	return instance
}

func referenceClient(t *testing.T, port int, transport string) miClient.Client {
	return referenceClientCredentials(t, port, transport, "alice", "native-business-secret")
}

func referenceClientCredentials(t *testing.T, port int, transport, username, password string) miClient.Client {
	t.Helper()
	protocol := appctlpb.TransportProtocol_TCP
	if transport == "UDP" {
		protocol = appctlpb.TransportProtocol_UDP
	}
	client := miClient.NewClient()
	err := client.Store(&miClient.ClientConfig{Profile: &appctlpb.ClientProfile{
		ProfileName:  proto.String("native-core-test"),
		Multiplexing: &appctlpb.MultiplexingConfig{Level: appctlpb.MultiplexingLevel_MULTIPLEXING_HIGH.Enum()},
		User:         &appctlpb.User{Name: proto.String(username), Password: proto.String(password)},
		Servers:      []*appctlpb.ServerEndpoint{{IpAddress: proto.String("127.0.0.1"), PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: protocol.Enum()}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })
	return client
}

func echoTarget(t *testing.T, network string) net.Addr {
	t.Helper()
	if network == "udp" {
		conn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		go func() {
			payload := make([]byte, 65535)
			for {
				n, peer, err := conn.ReadFrom(payload)
				if err != nil {
					return
				}
				_, _ = conn.WriteTo(payload[:n], peer)
			}
		}()
		return conn.LocalAddr()
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	return listener.Addr()
}

func TestNativeMieruOfficialClientPayloadUsesCoreDispatcher(t *testing.T) {
	for _, transport := range []string{"TCP", "UDP"} {
		for _, network := range []string{"tcp", "udp"} {
			t.Run(transport+"/"+network, func(t *testing.T) {
				port := reservePort(t, transport)
				instance := nativeCore(t, port, transport)
				client := referenceClient(t, port, transport)
				target := echoTarget(t, network)
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				conn, err := client.DialContext(ctx, target)
				if err != nil {
					t.Fatalf("official client native dial: %v", err)
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
				payload := bytes.Repeat([]byte("native-payload!"), 100)
				result := make([]byte, len(payload))
				if network == "tcp" {
					if _, err := conn.Write(payload); err != nil {
						t.Fatal(err)
					}
					if _, err := io.ReadFull(conn, result); err != nil {
						t.Fatal(err)
					}
				} else {
					packets := miCommon.NewUDPAssociateWrapper(miCommon.NewPacketOverStreamTunnel(conn))
					if _, err := packets.WriteTo(payload, target); err != nil {
						t.Fatal(err)
					}
					n, source, err := packets.ReadFrom(result)
					if err != nil || n != len(payload) || source.String() != target.String() {
						t.Fatalf("native UDP response %d %v %v", n, source, err)
					}
				}
				if !bytes.Equal(result, payload) {
					t.Fatal("target payload was corrupted")
				}
				manager := instance.GetFeature((*clientpolicy.Manager)(nil)).(clientpolicy.Manager)
				deadline := time.Now().Add(time.Second)
				for {
					snapshot, err := manager.Snapshot(nativeClientID)
					if err != nil {
						t.Fatal(err)
					}
					want := clientpolicy.Usage{RawUpload: uint64(len(payload)), RawDownload: uint64(len(payload)), BilledBytes: uint64(len(payload) * 3)}
					if snapshot.Usage == want {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("decrypted payload must share canonical ledger exactly once: got %v want %v", snapshot.Usage, want)
					}
					time.Sleep(time.Millisecond)
				}
				t.Logf("official mieru %s transport / %s payload %d bytes each direction through native core %s", transport, network, len(payload), fmt.Sprint(nativeClientID))
			})
		}
	}
}

func nativeIsTimeout(err error) bool {
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		return true
	}
	return miError.IsTimeout(err)
}
