package policy_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	S "github.com/sagernet/sing-snell"
	"github.com/sagernet/sing-snell/snellv4"
	"github.com/sagernet/sing-snell/snellv6"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/proxy"
	coreSnell "github.com/xtls/xray-core/proxy/snell"
)

const (
	snellOwner = "d395b8b1-31ab-47ea-9a67-e46dcc84cd96"
	snellPSK   = "native-snell-test-secret"
)

func snellMethod(t *testing.T, version int, psk string) S.Method {
	t.Helper()
	if version == 6 {
		c, err := snellv6.NewClient(snellv6.ClientOptions{PSK: []byte(psk)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c, err := snellv4.NewClient(snellv4.ClientOptions{PSK: []byte(psk)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNativeSnellInboundHTTPAndV6UnshapedTCPUDP(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			target, streamBytes := loopbackEchoTarget(t, "tcp")
			echo, packetBytes := snellPacketEcho(t, "127.0.0.1")
			listen := port(t)
			config := snellNativeConfig(version, listen)
			settings := config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
			var method S.Method
			var err error
			if version == 6 {
				settings["mode"] = "unshaped"
				method, err = snellv6.NewClient(snellv6.ClientOptions{PSK: []byte(snellPSK), Mode: snellv6.ModeUnshaped})
			} else {
				settings["obfs"] = "http"
				method, err = snellv4.NewClient(snellv4.ClientOptions{PSK: []byte(snellPSK), ObfsMode: S.ObfsModeHTTP, ObfsHost: "native-snell.test"})
			}
			if err != nil {
				t.Fatal(err)
			}
			instance := start(t, loopbackJSON(t, config))
			raw, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
			if err != nil {
				t.Fatal(err)
			}
			c, err := method.DialConn(raw, M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", target)))
			if err != nil {
				raw.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			payload := []byte("mode-stream")
			exchange(t, c, payload)
			packetRaw, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
			if err != nil {
				t.Fatal(err)
			}
			pc, err := method.DialPacketConn(packetRaw)
			if err != nil {
				packetRaw.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() { pc.Close() })
			for _, packet := range [][]byte{bytes.Repeat([]byte{0x69}, 13000), {}} {
				pc.SetDeadline(time.Now().Add(2 * time.Second))
				if _, err := pc.WriteTo(packet, echo.LocalAddr()); err != nil {
					t.Fatal(err)
				}
				b := make([]byte, 65535)
				n, source, err := pc.ReadFrom(b)
				if err != nil || !bytes.Equal(packet, b[:n]) || source.String() != echo.LocalAddr().String() {
					t.Fatalf("mode UDP payload/source mismatch: length=%d source=%v err=%v", n, source, err)
				}
			}
			expected := uint64(len(payload) + 13000)
			snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
			if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: expected, RawDownload: expected, BilledBytes: expected * 3}) || streamBytes.Load() != int64(len(payload)) || packetBytes.Load() != 13000 {
				t.Fatalf("mode payload ledger mismatch: %+v err=%v", snap, err)
			}
		})
	}
}

func TestNativeSnellCredentialRemovalRotationAndSibling(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			listen, siblingPort := port(t), port(t)
			config := snellNativeConfig(version, listen)
			config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "sibling", "listen": "127.0.0.1", "port": siblingPort, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": "tcp", "rewriteAddress": "127.0.0.1", "rewritePort": target, "clientId": snellOwner}})
			instance := start(t, loopbackJSON(t, config))
			flow := snellFlow(t, version, listen, target, snellPSK)
			sibling := loopbackFlow(t, "tcp", siblingPort)
			exchange(t, flow, []byte("snell!"))
			exchange(t, sibling, []byte("siblng"))
			idle, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
			if err != nil {
				t.Fatal(err)
			}
			defer idle.Close()
			manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
			handler, err := manager.GetHandler(context.Background(), "snell")
			if err != nil {
				t.Fatal(err)
			}
			if err := (&command.RemoveUserOperation{Email: "snell-owner"}).ApplyInbound(context.Background(), handler); err != nil {
				t.Fatal(err)
			}
			assertPasswordProxyClosed(t, flow)
			assertPasswordProxyClosed(t, idle)
			users := handler.(proxy.GetInbound).GetInbound().(proxy.UserManager)
			if users.GetUsersCount(context.Background()) != 0 {
				t.Fatal("removed owner retained")
			}
			exchange(t, sibling, []byte("alive!"))
			operation := &command.AddUserOperation{User: &protocol.User{Email: "snell-owner", ClientId: snellOwner, Account: serial.ToTypedMessage(&coreSnell.Account{Psk: "native-snell-replacement-secret"})}}
			if err := operation.ApplyInbound(context.Background(), handler); err != nil {
				t.Fatal(err)
			}
			old := snellFlow(t, version, listen, target, snellPSK)
			old.SetDeadline(time.Now().Add(time.Second))
			old.Write([]byte("denied"))
			if _, err := old.Read(make([]byte, 1)); err == nil {
				t.Fatal("old PSK survived replacement")
			}
			fresh := snellFlow(t, version, listen, target, "native-snell-replacement-secret")
			exchange(t, fresh, []byte("fresh!"))
			snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
			if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 24, RawDownload: 24, BilledBytes: 72}) || received.Load() != 24 {
				t.Fatalf("replacement changed canonical ledger or sibling: %+v err=%v target=%d", snap, err, received.Load())
			}
			if err := manager.RemoveHandler(context.Background(), "snell"); err != nil {
				t.Fatal(err)
			}
			assertPasswordProxyClosed(t, fresh)
			if err := operation.ApplyInbound(context.Background(), handler); err == nil {
				t.Fatal("closed Snell handler accepted owner")
			}
			exchange(t, sibling, []byte("again!"))
		})
	}
}

func TestNativeSnellQuotaExpiryAndMissingPolicy(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		for _, mode := range []string{"quota", "expiry", "missing"} {
			t.Run(fmt.Sprintf("v%d/%s", version, mode), func(t *testing.T) {
				target, received := loopbackEchoTarget(t, "tcp")
				listen := port(t)
				config := snellNativeConfig(version, listen)
				policy := config["clientPolicy"].(map[string]any)["policies"].([]any)[0].(map[string]any)
				if mode == "quota" {
					policy["quotaBytes"] = 1536
					policy["multiplierMicros"] = 2000000
				}
				if mode == "missing" {
					delete(config, "clientPolicy")
				}
				instance := start(t, loopbackJSON(t, config))
				flow := snellFlow(t, version, listen, target, snellPSK)
				if mode == "missing" {
					flow.SetDeadline(time.Now().Add(time.Second))
					flow.Write([]byte("denied"))
					if _, err := flow.Read(make([]byte, 1)); err == nil {
						t.Fatal("missing policy admitted payload")
					}
					if received.Load() != 0 {
						t.Fatal("missing policy dialed target")
					}
					return
				}
				exchange(t, flow, bytes.Repeat([]byte{0x33}, 256))
				engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
				if mode == "quota" {
					flow.SetDeadline(time.Now().Add(time.Second))
					flow.Write(bytes.Repeat([]byte{0x34}, 256))
					b := make([]byte, 256)
					_, _ = flow.Read(b)
					snap, err := engine.Snapshot(snellOwner)
					if err != nil || snap.Usage.BilledBytes != 1536 || snap.Usage.RawUpload+snap.Usage.RawDownload != 768 || snap.Reasons != clientpolicy.ReasonQuota {
						t.Fatalf("native quota exceeded/failed: %+v err=%v", snap, err)
					}
				} else {
					p, _, err := engine.GetClient(snellOwner)
					if err != nil {
						t.Fatal(err)
					}
					p.Version++
					p.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
					if err := engine.Apply(p); err != nil {
						t.Fatal(err)
					}
					assertPasswordProxyClosed(t, flow)
				}
				fresh := snellFlow(t, version, listen, target, snellPSK)
				fresh.SetDeadline(time.Now().Add(time.Second))
				fresh.Write([]byte("denied"))
				_, err := fresh.Read(make([]byte, 1))
				var timeout net.Error
				if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
					t.Fatalf("restricted Snell reconnect stayed active: %v", err)
				}
			})
		}
	}
}

func TestNativeSnellOutboundUDPFirst13kEmptyExactLedger(t *testing.T) {
	const nextOwner = "715fe26d-75d4-40a1-b7a9-abd7667c4202"
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			echo, received := snellPacketEcho(t, "127.0.0.1")
			origin, next := port(t), port(t)
			config := snellNativeConfig(version, origin)
			config["clientPolicy"].(map[string]any)["policies"] = append(config["clientPolicy"].(map[string]any)["policies"].([]any), map[string]any{"clientId": nextOwner, "version": 1, "enabled": true, "multiplierMicros": 1500000, "burstBytes": 65536})
			config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "next", "listen": "127.0.0.1", "port": next, "protocol": "snell", "settings": map[string]any{"version": version, "psk": "native-snell-chain-secret", "clientId": nextOwner, "email": "next-owner"}})
			config["outbounds"] = append(config["outbounds"].([]any), map[string]any{"tag": "snell-next", "protocol": "snell", "settings": map[string]any{"version": version, "psk": "native-snell-chain-secret", "address": "127.0.0.1", "port": next}})
			config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"snell"}, "outboundTag": "snell-next"}}}
			instance := start(t, loopbackJSON(t, config))
			pc := snellUDP(t, version, origin)
			for _, p := range [][]byte{bytes.Repeat([]byte{0x73}, 13000), {}, []byte("chain")} {
				pc.SetDeadline(time.Now().Add(2 * time.Second))
				if _, err := pc.WriteTo(p, echo.LocalAddr()); err != nil {
					t.Fatal(err)
				}
				b := make([]byte, 65535)
				n, _, err := pc.ReadFrom(b)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(p, b[:n]) {
					t.Fatalf("native outbound datagram split/truncated/coalesced: got=%d want=%d", n, len(p))
				}
			}
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			for _, owner := range []string{snellOwner, nextOwner} {
				snap, err := engine.Snapshot(owner)
				if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 13005, RawDownload: 13005, BilledBytes: 39015}) {
					t.Fatalf("native UDP chain canonical ledger mismatch: owner=%s snapshot=%+v err=%v", owner, snap, err)
				}
			}
			if received.Load() != 13005 {
				t.Fatal("native UDP chain changed target bytes")
			}
		})
	}
}

func TestNativeSnellWireIDsShareCanonicalRateAndLedger(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			listen := port(t)
			config := snellNativeConfig(version, listen)
			p := config["clientPolicy"].(map[string]any)["policies"].([]any)[0].(map[string]any)
			p["uploadBytesPerSecond"] = 4096
			p["burstBytes"] = 4096
			instance := start(t, loopbackJSON(t, config))
			var flows []net.Conn
			for _, wireID := range []string{"pretended-first-user", "pretended-second-user"} {
				raw, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
				if err != nil {
					t.Fatal(err)
				}
				var method S.Method
				if version == 6 {
					method, err = snellv6.NewClient(snellv6.ClientOptions{PSK: []byte(snellPSK), UserKey: []byte(wireID)})
				} else {
					method, err = snellv4.NewClient(snellv4.ClientOptions{PSK: []byte(snellPSK), UserKey: []byte(wireID)})
				}
				if err != nil {
					t.Fatal(err)
				}
				c, err := method.DialConn(raw, M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", target)))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { c.Close() })
				flows = append(flows, c)
			}
			payload := bytes.Repeat([]byte{0x64}, 4096)
			exchange(t, flows[0], payload)
			started := time.Now()
			exchange(t, flows[1], payload)
			elapsed := time.Since(started)
			if elapsed < 700*time.Millisecond {
				t.Fatalf("wire IDs acquired separate canonical upload buckets: elapsed=%s", elapsed)
			}
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			snap, err := engine.Snapshot(snellOwner)
			if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 8192, RawDownload: 8192, BilledBytes: 24576}) || received.Load() != 8192 {
				t.Fatalf("wire ID changed trusted listener identity: %+v err=%v received=%d", snap, err, received.Load())
			}
			if _, err := engine.Snapshot("pretended-first-user"); err == nil {
				t.Fatal("untrusted wire ID became a separate ledger")
			}
		})
	}
}

func TestNativeSnellUDPDisableExpiryQuotaAndCredentialCleanup(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		for _, mode := range []string{"disable", "expiry", "quota", "remove"} {
			t.Run(fmt.Sprintf("v%d/%s", version, mode), func(t *testing.T) {
				echo, _ := snellPacketEcho(t, "127.0.0.1")
				listen := port(t)
				config := snellNativeConfig(version, listen)
				if mode == "quota" {
					config["clientPolicy"].(map[string]any)["policies"].([]any)[0].(map[string]any)["quotaBytes"] = 36
				}
				instance := start(t, loopbackJSON(t, config))
				pc := snellUDP(t, version, listen)
				pc.SetDeadline(time.Now().Add(time.Second))
				if _, err := pc.WriteTo([]byte("packet"), echo.LocalAddr()); err != nil {
					t.Fatal(err)
				}
				if _, _, err := pc.ReadFrom(make([]byte, 32)); err != nil {
					t.Fatal(err)
				}
				engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
				if mode == "remove" {
					manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
					handler, err := manager.GetHandler(context.Background(), "snell")
					if err != nil {
						t.Fatal(err)
					}
					if err := (&command.RemoveUserOperation{Email: "snell-owner"}).ApplyInbound(context.Background(), handler); err != nil {
						t.Fatal(err)
					}
				} else if mode == "quota" {
					_, _ = pc.WriteTo([]byte("packet"), echo.LocalAddr())
					_, _, _ = pc.ReadFrom(make([]byte, 32))
				} else {
					p, _, err := engine.GetClient(snellOwner)
					if err != nil {
						t.Fatal(err)
					}
					p.Version++
					if mode == "disable" {
						p.Enabled = false
					} else {
						p.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
					}
					if err := engine.Apply(p); err != nil {
						t.Fatal(err)
					}
				}
				pc.SetReadDeadline(time.Now().Add(time.Second))
				_, _, err := pc.ReadFrom(make([]byte, 32))
				var timeout net.Error
				if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
					t.Fatalf("restricted/removed UDP association remained open: %v", err)
				}
				snap, err := engine.Snapshot(snellOwner)
				if err != nil || snap.ActiveSessions != 0 {
					t.Fatalf("UDP association retained policy resources: %+v err=%v", snap, err)
				}
				if mode == "quota" && (snap.Usage.BilledBytes != 36 || snap.Reasons != clientpolicy.ReasonQuota) {
					t.Fatalf("UDP exceeded canonical quota: %+v", snap)
				}
			})
		}
	}
}

func TestNativeSnellUDPWireBoundary(t *testing.T) {
	for _, version := range []int{4, 5} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			echo, received := snellPacketEcho(t, "127.0.0.1")
			listen := port(t)
			instance := start(t, loopbackJSON(t, snellNativeConfig(version, listen)))
			pc := snellUDP(t, version, listen)
			pc.SetDeadline(time.Now().Add(2 * time.Second))
			payload := bytes.Repeat([]byte{0x21}, 16374)
			if _, err := pc.WriteTo(payload, echo.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			b := make([]byte, 65535)
			n, _, err := pc.ReadFrom(b)
			if err != nil || !bytes.Equal(payload, b[:n]) {
				t.Fatalf("max IPv4 request payload changed: got=%d err=%v", n, err)
			}
			if _, err := pc.WriteTo(append(payload, 0x21), echo.LocalAddr()); err == nil {
				t.Fatal("payload exceeded genuine16383-byte record boundary")
			}
			snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
			if err != nil || snap.Usage.RawUpload != 16374 || snap.Usage.RawDownload != 16374 || received.Load() != 16374 {
				t.Fatalf("oversized datagram was admitted/truncated: %+v err=%v target=%d", snap, err, received.Load())
			}
		})
	}
}

func snellUDP(t *testing.T, version, listen int) net.PacketConn {
	t.Helper()
	raw, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	pc, err := snellMethod(t, version, snellPSK).DialPacketConn(raw)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	return pc
}

func snellPacketEcho(t *testing.T, host string) (net.PacketConn, *atomic.Int64) {
	t.Helper()
	c, err := net.ListenPacket("udp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	received := new(atomic.Int64)
	go func() {
		b := make([]byte, 65535)
		for {
			n, addr, err := c.ReadFrom(b)
			if err != nil {
				return
			}
			received.Add(int64(n))
			c.WriteTo(b[:n], addr)
		}
	}()
	return c, received
}

func TestNativeSnellUDPVersions(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			echo, received := snellPacketEcho(t, "127.0.0.1")
			listen := port(t)
			instance := start(t, loopbackJSON(t, snellNativeConfig(version, listen)))
			pc := snellUDP(t, version, listen)
			payload := []byte("datagram")
			pc.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := pc.WriteTo(payload, echo.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			b := make([]byte, 65535)
			n, source, err := pc.ReadFrom(b)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(payload, b[:n]) || source.String() != echo.LocalAddr().String() {
				t.Fatalf("UDP payload/source changed: bytes=%d source=%s", n, source)
			}
			snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
			if err != nil || snap.Usage.RawUpload != 8 || snap.Usage.RawDownload != 8 || received.Load() != 8 {
				t.Fatalf("UDP payload identity/accounting: %+v err=%v received=%d", snap, err, received.Load())
			}
		})
	}
}

func TestNativeSnellRespectsHandshakeTimeout(t *testing.T) {
	listen := port(t)
	config := snellNativeConfig(6, listen)
	config["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{"handshake": 1, "connIdle": 1, "uplinkOnly": 1, "downlinkOnly": 1}}}
	start(t, loopbackJSON(t, config))
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = c.Read(make([]byte, 1))
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("Snell ignored native handshake policy timeout: %v", err)
	}
}

func TestNativeSnellUDPFirst13kEmptyDomainIPv6SourceAndRoutePolicy(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			v4, received4 := snellPacketEcho(t, "127.0.0.1")
			v6, received6 := snellPacketEcho(t, "::1")
			denied, deniedBytes := snellPacketEcho(t, "127.0.0.1")
			listen := port(t)
			config := snellNativeConfig(version, listen)
			config["dns"] = map[string]any{"hosts": map[string]any{"snell-target.test": "127.0.0.1", "denied-snell.test": "127.0.0.1"}}
			config["outbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["domainStrategy"] = "UseIP"
			config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "domain": []string{"full:denied-snell.test"}, "outboundTag": "block"}, map[string]any{"type": "field", "port": fmt.Sprint(denied.LocalAddr().(*net.UDPAddr).Port), "outboundTag": "block"}}}
			instance := start(t, loopbackJSON(t, config))
			pc := snellUDP(t, version, listen)
			domain := M.ParseSocksaddrHostPort("snell-target.test", uint16(v4.LocalAddr().(*net.UDPAddr).Port))
			var payloadBytes uint64
			for _, destination := range []struct{ addr, source net.Addr }{{v4.LocalAddr(), v4.LocalAddr()}, {domain, v4.LocalAddr()}, {v6.LocalAddr(), v6.LocalAddr()}} {
				addr := destination.addr
				payloads := [][]byte{bytes.Repeat([]byte{0x78}, 13000), []byte("packet"), bytes.Repeat([]byte{0x77}, 1024), {}}
				for _, payload := range payloads {
					pc.SetDeadline(time.Now().Add(2 * time.Second))
					if _, err := pc.WriteTo(payload, addr); err != nil {
						t.Fatal(err)
					}
					b := make([]byte, 65535)
					n, source, err := pc.ReadFrom(b)
					if err != nil {
						t.Fatalf("UDP %s/%d: %v", addr, len(payload), err)
					}
					if !bytes.Equal(payload, b[:n]) {
						t.Fatalf("UDP packet split/truncated/coalesced: destination=%s want=%d got=%d", addr, len(payload), n)
					}
					if _, ok := source.(*net.UDPAddr); !ok {
						t.Fatalf("UDP response source is not an IP: %T %v", source, source)
					}
					if source.String() != destination.source.String() {
						t.Fatalf("UDP response source changed: destination=%s source=%s want=%s", addr, source, destination.source)
					}
					payloadBytes += uint64(len(payload))
				}
			}
			snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
			if err != nil || snap.Usage.RawUpload != payloadBytes || snap.Usage.RawDownload != payloadBytes || uint64(received4.Load()+received6.Load()) != payloadBytes {
				t.Fatalf("UDP datagrams changed payload ledger: %+v expected=%d err=%v", snap, payloadBytes, err)
			}
			for _, addr := range []net.Addr{denied.LocalAddr(), M.ParseSocksaddrHostPort("denied-snell.test", uint16(v4.LocalAddr().(*net.UDPAddr).Port))} {
				blocked := snellUDP(t, version, listen)
				blocked.SetDeadline(time.Now().Add(150 * time.Millisecond))
				_, _ = blocked.WriteTo([]byte("denied"), addr)
				if _, _, err := blocked.ReadFrom(make([]byte, 64)); err == nil {
					t.Fatalf("denied UDP destination returned data: %v", addr)
				}
				blocked.Close()
			}
			if deniedBytes.Load() != 0 || uint64(received4.Load()+received6.Load()) != payloadBytes {
				t.Fatal("destination/domain route deny bypassed dispatcher")
			}
		})
	}
}

func snellNativeConfig(version, listen int) map[string]any {
	return map[string]any{
		"log":          map[string]any{"loglevel": "error"},
		"clientPolicy": map[string]any{"policies": []any{map[string]any{"clientId": snellOwner, "version": 1, "enabled": true, "multiplierMicros": 1500000, "burstBytes": 65536}}},
		"inbounds":     []any{map[string]any{"tag": "snell", "listen": "127.0.0.1", "port": listen, "protocol": "snell", "settings": map[string]any{"version": version, "psk": snellPSK, "clientId": snellOwner, "email": "snell-owner"}}},
		"outbounds":    []any{map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}}}, map[string]any{"tag": "block", "protocol": "blackhole"}},
	}
}

func snellFlow(t *testing.T, version, listen, target int, psk string) net.Conn {
	t.Helper()
	raw, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listen), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c, err := snellMethod(t, version, psk).DialConn(raw, M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", target)))
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestNativeSnellTCPVersionsPolicyAndRouteDeny(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			listen := port(t)
			config := snellNativeConfig(version, listen)
			config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "port": fmt.Sprint(target + 1), "outboundTag": "block"}}}
			instance := start(t, loopbackJSON(t, config))
			flow := snellFlow(t, version, listen, target, snellPSK)
			exchange(t, flow, bytes.Repeat([]byte{0x53}, 12000))
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			snap, err := engine.Snapshot(snellOwner)
			if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 12000, RawDownload: 12000, BilledBytes: 36000}) || snap.ActiveSessions != 1 {
				t.Fatalf("decoded Snell payload identity/accounting: %+v err=%v", snap, err)
			}
			bad := snellFlow(t, version, listen, target, "wrong-native-secret")
			bad.SetDeadline(time.Now().Add(time.Second))
			bad.Write([]byte("denied"))
			if _, err := bad.Read(make([]byte, 1)); err == nil {
				t.Fatal("wrong PSK returned target payload")
			}
			blocked := snellFlow(t, version, listen, target+1, snellPSK)
			blocked.SetDeadline(time.Now().Add(time.Second))
			blocked.Write([]byte("blocked"))
			if _, err := blocked.Read(make([]byte, 1)); err == nil {
				t.Fatal("route deny returned data")
			}
			if received.Load() != 12000 {
				t.Fatalf("unverified or denied payload reached target: %d", received.Load())
			}
			policy, _, err := engine.GetClient(snellOwner)
			if err != nil {
				t.Fatal(err)
			}
			policy.Version++
			policy.Enabled = false
			if err := engine.Apply(policy); err != nil {
				t.Fatal(err)
			}
			assertPasswordProxyClosed(t, flow)
		})
	}
}

func TestNativeSnellOutboundVersions(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			target := tcpEcho(t).Addr().(*net.TCPAddr).Port
			serverPort, tunnelPort := port(t), port(t)
			config := snellNativeConfig(version, serverPort)
			config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "tunnel", "listen": "127.0.0.1", "port": tunnelPort, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": "tcp", "rewriteAddress": "127.0.0.1", "rewritePort": target}})
			config["outbounds"] = append(config["outbounds"].([]any), map[string]any{"tag": "snell-out", "protocol": "snell", "settings": map[string]any{"version": version, "psk": snellPSK, "address": "127.0.0.1", "port": serverPort, "reuse": true}})
			config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"tunnel"}, "outboundTag": "snell-out"}}}
			instance := start(t, loopbackJSON(t, config))
			flow := loopbackFlow(t, "tcp", tunnelPort)
			exchange(t, flow, []byte("native outbound"))
			snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
			if err != nil || snap.Usage.RawUpload != 15 || snap.Usage.RawDownload != 15 {
				t.Fatalf("Snell outbound did not decode through server: %+v err=%v", snap, err)
			}
		})
	}
}

type nativeReuseDialer struct{ count atomic.Int32 }

func (d *nativeReuseDialer) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	d.count.Add(1)
	return (&net.Dialer{}).DialContext(ctx, network, dest.String())
}

func (*nativeReuseDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unused")
}

func TestNativeSnellTCPReuseIdleStartsAfterLogicalCompletion(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			target, _ := loopbackEchoTarget(t, "tcp")
			listen := port(t)
			config := snellNativeConfig(version, listen)
			config["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{"handshake": 1, "connIdle": 1, "uplinkOnly": 1, "downlinkOnly": 1}}}
			start(t, loopbackJSON(t, config))
			d := new(nativeReuseDialer)
			var method interface {
				DialContext(context.Context, M.Socksaddr) (net.Conn, error)
				Close() error
			}
			var err error
			if version == 6 {
				method, err = snellv6.NewClient(snellv6.ClientOptions{PSK: []byte(snellPSK), Reuse: true, Dialer: d, Server: M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", listen))})
			} else {
				method, err = snellv4.NewClient(snellv4.ClientOptions{PSK: []byte(snellPSK), Reuse: true, Dialer: d, Server: M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", listen))})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer method.Close()
			dest := M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", target))
			c, err := method.DialContext(context.Background(), dest)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(4 * time.Second))
			for range 14 {
				exchange(t, c, []byte("active"))
				time.Sleep(100 * time.Millisecond)
			}
			if err = N.CloseWrite(c); err != nil {
				t.Fatal(err)
			}
			if _, err = io.ReadAll(c); err != nil {
				t.Fatal(err)
			}
			c.SetDeadline(time.Time{})
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
			c2, err := method.DialContext(context.Background(), dest)
			if err != nil {
				t.Fatal(err)
			}
			defer c2.Close()
			c2.SetDeadline(time.Now().Add(time.Second))
			if _, err = c2.Write([]byte("next")); err != nil {
				t.Fatalf("reused socket failed 100ms after active logical request finished (idle policy=1s): %v; dials=%d", err, d.count.Load())
			}
			b := make([]byte, 4)
			if _, err = io.ReadFull(c2, b); err != nil {
				t.Fatalf("reuse reply failed 100ms after active logical request finished (idle policy=1s): %v; dials=%d", err, d.count.Load())
			}
			if string(b) != "next" || d.count.Load() != 1 {
				t.Fatalf("unexpected bytes/dials: %q/%d", b, d.count.Load())
			}
		})
	}
}

func TestNativeSnellUDPDownlinkRefreshesIdle(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			target, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			stop := make(chan struct{})
			defer close(stop)
			go func() {
				b := make([]byte, 16)
				_, addr, err := target.ReadFrom(b)
				if err != nil {
					return
				}
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
						if _, err = target.WriteTo([]byte("downstream"), addr); err != nil {
							return
						}
					}
				}
			}()
			listen := port(t)
			config := snellNativeConfig(version, listen)
			config["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{"handshake": 1, "connIdle": 1, "uplinkOnly": 1, "downlinkOnly": 1}}}
			start(t, loopbackJSON(t, config))
			pc := snellUDP(t, version, listen)
			if _, err = pc.WriteTo([]byte("start"), target.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			pc.SetReadDeadline(time.Now().Add(3 * time.Second))
			started := time.Now()
			for j := 0; j < 15; j++ {
				n, _, err := pc.ReadFrom(make([]byte, 64))
				if err != nil {
					t.Fatalf("UDP closed during continuous 100ms downlink: packets=%d elapsed=%s bytes=%d err=%v", j, time.Since(started), n, err)
				}
			}
		})
	}
}
