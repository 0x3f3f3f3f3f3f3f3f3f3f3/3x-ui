package policy_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	S "github.com/sagernet/sing-snell"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/features/inbound"
	coreSnell "github.com/xtls/xray-core/proxy/snell"
	"github.com/xtls/xray-core/testing/testauthority"
)

func snellQUICClient(t *testing.T, listen int) net.Conn {
	t.Helper()
	c, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func snellQUICExchange(t *testing.T, c net.Conn, wire, expected []byte) {
	t.Helper()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(wire); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 65535)
	n, err := c.Read(b)
	if err != nil || !bytes.Equal(expected, b[:n]) {
		t.Fatalf("QUIC whole datagram changed: want=%d got=%d err=%v", len(expected), n, err)
	}
}

func TestNativeSnellV5QUICFirst13kDomainIPv6SourceIsolationAndRouteDeny(t *testing.T) {
	v4, received4 := snellPacketEcho(t, "127.0.0.1")
	v6, received6 := snellPacketEcho(t, "::1")
	denied, deniedBytes := snellPacketEcho(t, "127.0.0.1")
	listen := port(t)
	config := snellNativeConfig(5, listen)
	config["dns"] = map[string]any{"hosts": map[string]any{"snell-quic.test": "127.0.0.1", "denied-snell-quic.test": "127.0.0.1"}}
	config["outbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["domainStrategy"] = "UseIP"
	config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "domain": []string{"full:denied-snell-quic.test"}, "outboundTag": "block"}, map[string]any{"type": "field", "port": fmt.Sprint(denied.LocalAddr().(*net.UDPAddr).Port), "outboundTag": "block"}}}
	instance := start(t, loopbackJSON(t, config))
	domain := M.ParseSocksaddrHostPort("snell-quic.test", uint16(v4.LocalAddr().(*net.UDPAddr).Port))
	initial := snellQUICInitial(13000)
	raw := append([]byte{0x40}, bytes.Repeat([]byte{0x73}, 511)...)
	var clients []net.Conn
	for _, target := range []net.Addr{v4.LocalAddr(), domain, v6.LocalAddr()} {
		c := snellQUICClient(t, listen)
		clients = append(clients, c)
		envelope := snellQUICEnvelope(t, snellPSK, target, initial, "same-untrusted-wire-id")
		snellQUICExchange(t, c, envelope, initial)
		snellQUICExchange(t, c, raw, raw)
	}
	// Repeated authentication unwraps the packet, but cannot retarget an
	// existing source association. The IPv6 client remains independently bound.
	snellQUICExchange(t, clients[0], snellQUICEnvelope(t, snellPSK, v6.LocalAddr(), raw, "another-wire-id"), raw)
	snellQUICExchange(t, clients[2], raw, raw)
	if received4.Load() != 27536 || received6.Load() != 14024 {
		t.Fatalf("source targets/first13k changed: v4=%d v6=%d", received4.Load(), received6.Load())
	}
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	snap, err := engine.Snapshot(snellOwner)
	if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 41560, RawDownload: 41560, BilledBytes: 124680}) || snap.ActiveSessions != 3 {
		t.Fatalf("source associations created independent identities: %+v err=%v", snap, err)
	}
	for _, target := range []net.Addr{denied.LocalAddr(), M.ParseSocksaddrHostPort("denied-snell-quic.test", uint16(v4.LocalAddr().(*net.UDPAddr).Port))} {
		c := snellQUICClient(t, listen)
		c.SetDeadline(time.Now().Add(100 * time.Millisecond))
		c.Write(snellQUICEnvelope(t, snellPSK, target, initial, "forged-id"))
		if _, err := c.Read(make([]byte, 65535)); err == nil {
			t.Fatal("domain/IP port deny bypassed Dispatcher")
		}
	}
	// Neither a raw first datagram nor a bad PSK may inherit an existing source's
	// authentication. An empty first datagram reaches the opt-in hub intact but
	// does not establish a QUIC session.
	for _, invalid := range [][]byte{raw, {}, snellQUICEnvelope(t, "wrong-quic-test-secret", v4.LocalAddr(), initial, "same-untrusted-wire-id")} {
		c := snellQUICClient(t, listen)
		c.SetDeadline(time.Now().Add(100 * time.Millisecond))
		c.Write(invalid)
		if _, err := c.Read(make([]byte, 65535)); err == nil {
			t.Fatal("unauthenticated source established a QUIC association")
		}
	}
	if deniedBytes.Load() != 0 || received4.Load() != 27536 || received6.Load() != 14024 {
		t.Fatal("invalid source or denied target received payload")
	}
}

func TestNativeSnellV5QUICDisableExpiryQuotaRemovalAndHandlerCleanup(t *testing.T) {
	for _, mode := range []string{"disable", "expiry", "quota", "remove", "handler"} {
		t.Run(mode, func(t *testing.T) {
			echo, received := snellPacketEcho(t, "127.0.0.1")
			listen := port(t)
			config := snellNativeConfig(5, listen)
			if mode == "quota" {
				config["clientPolicy"].(map[string]any)["policies"].([]any)[0].(map[string]any)["quotaBytes"] = 3072
			}
			instance := start(t, loopbackJSON(t, config))
			c := snellQUICClient(t, listen)
			packet := append([]byte{0x40}, bytes.Repeat([]byte{0x75}, 511)...)
			snellQUICExchange(t, c, snellQUICEnvelope(t, snellPSK, echo.LocalAddr(), packet, "untrusted-quic-id"), packet)
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			if mode == "remove" || mode == "handler" {
				manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
				handler, err := manager.GetHandler(context.Background(), "snell")
				if err != nil {
					t.Fatal(err)
				}
				if mode == "remove" {
					err = (&command.RemoveUserOperation{Email: "snell-owner"}).ApplyInbound(context.Background(), handler)
				} else {
					err = manager.RemoveHandler(context.Background(), "snell")
				}
				if err != nil {
					t.Fatal(err)
				}
			} else if mode == "quota" {
				c.SetDeadline(time.Now().Add(200 * time.Millisecond))
				c.Write(packet)
				c.Read(make([]byte, 65535))
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
				if err := testauthority.Apply(t, engine, p); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(time.Second)
			for {
				snap, err := engine.Snapshot(snellOwner)
				if err != nil {
					t.Fatal(err)
				}
				if snap.ActiveSessions == 0 {
					if mode == "quota" && (snap.Usage.BilledBytes != 3072 || snap.Reasons != (clientpolicy.ReasonQuota|clientpolicy.ReasonAuthority)) {
						t.Fatalf("native QUIC exceeded quota: %+v", snap)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("native QUIC association retained policy resources")
				}
				time.Sleep(time.Millisecond)
			}
			before := received.Load()
			c.SetDeadline(time.Now().Add(100 * time.Millisecond))
			c.Write(packet)
			if _, err := c.Read(make([]byte, 65535)); err == nil || received.Load() != before {
				t.Fatal("revoked QUIC source continued forwarding")
			}
		})
	}
}

func TestNativeSnellV5QUICMissingDisabledExpiredPolicyRejectsFreshSource(t *testing.T) {
	for _, mode := range []string{"missing", "disabled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			echo, received := snellPacketEcho(t, "127.0.0.1")
			listen := port(t)
			config := snellNativeConfig(5, listen)
			p := config["clientPolicy"].(map[string]any)["policies"].([]any)[0].(map[string]any)
			if mode == "missing" {
				delete(config, "clientPolicy")
			} else if mode == "disabled" {
				p["enabled"] = false
			} else {
				p["expiresAt"] = time.Now().Add(-time.Second).UnixMilli()
			}
			instance := start(t, loopbackJSON(t, config))
			c := snellQUICClient(t, listen)
			c.SetDeadline(time.Now().Add(100 * time.Millisecond))
			c.Write(snellQUICEnvelope(t, snellPSK, echo.LocalAddr(), snellQUICInitial(1200), "fresh-forged-id"))
			if _, err := c.Read(make([]byte, 65535)); err == nil || received.Load() != 0 {
				t.Fatal("missing/disabled/expired canonical policy admitted a fresh QUIC source")
			}
			if mode != "missing" {
				snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
				if err != nil || snap.ActiveSessions != 0 || snap.Usage != (clientpolicy.Usage{}) {
					t.Fatalf("rejected fresh QUIC source retained policy resources or usage: %+v err=%v", snap, err)
				}
			}
		})
	}
}

func TestNativeSnellV5QUICWireIDsShareDirectionalRates(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			echo, received := snellPacketEcho(t, "127.0.0.1")
			listen := port(t)
			config := snellNativeConfig(5, listen)
			p := config["clientPolicy"].(map[string]any)["policies"].([]any)[0].(map[string]any)
			p[direction+"BytesPerSecond"] = 4096
			p["burstBytes"] = 4096
			instance := start(t, loopbackJSON(t, config))
			payload := snellQUICInitial(4096)
			first, second := snellQUICClient(t, listen), snellQUICClient(t, listen)
			snellQUICExchange(t, first, snellQUICEnvelope(t, snellPSK, echo.LocalAddr(), payload, "first-forged-id"), payload)
			started := time.Now()
			snellQUICExchange(t, second, snellQUICEnvelope(t, snellPSK, echo.LocalAddr(), payload, "second-forged-id"), payload)
			if time.Since(started) < 700*time.Millisecond {
				t.Fatal("wire IDs obtained separate directional rate buckets")
			}
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			snap, err := engine.Snapshot(snellOwner)
			if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 8192, RawDownload: 8192, BilledBytes: 24576}) || received.Load() != 8192 {
				t.Fatalf("native QUIC directional rate ledger: %+v target=%d err=%v", snap, received.Load(), err)
			}
			if _, err = engine.Snapshot("first-forged-id"); err == nil {
				t.Fatal("wire ID became a ledger identity")
			}
		})
	}
}

func TestNativeSnellV5QUICDownlinkIdleRefreshAndAssociationReclaim(t *testing.T) {
	target, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		b := make([]byte, 65535)
		_, addr, err := target.ReadFrom(b)
		if err != nil {
			return
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for n := 0; n < 15; n++ {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if _, err := target.WriteTo([]byte{0x40, 0x71}, addr); err != nil {
					return
				}
			}
		}
	}()
	listen := port(t)
	config := snellNativeConfig(5, listen)
	config["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{"handshake": 1, "connIdle": 1, "uplinkOnly": 1, "downlinkOnly": 1}}}
	instance := start(t, loopbackJSON(t, config))
	c := snellQUICClient(t, listen)
	c.Write(snellQUICEnvelope(t, snellPSK, target.LocalAddr(), []byte{0x40, 0x71}, "idle-wire-id"))
	c.SetDeadline(time.Now().Add(3 * time.Second))
	for j := 0; j < 15; j++ {
		b := make([]byte, 16)
		if n, err := c.Read(b); err != nil || n != 2 {
			t.Fatalf("continuous QUIC downlink exceeded idle timer: packet=%d length=%d err=%v", j, n, err)
		}
	}
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap, err := engine.Snapshot(snellOwner)
		if err != nil {
			t.Fatal(err)
		}
		if snap.ActiveSessions == 0 {
			if snap.Usage.RawUpload != 2 || snap.Usage.RawDownload != 30 {
				t.Fatalf("idle QUIC payload ledger changed: %+v", snap)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle QUIC association did not release CPE session")
		}
		time.Sleep(time.Millisecond)
	}
	// The same UDP source can authenticate a new target after its old binding is
	// reaped. Closing a remote UDP socket alone has no on-wire EOF notification.
	echo, _ := snellPacketEcho(t, "127.0.0.1")
	raw := []byte{0x40, 0x76}
	snellQUICExchange(t, c, snellQUICEnvelope(t, snellPSK, echo.LocalAddr(), raw, "fresh-wire-id"), raw)
}

func TestNativeSnellV5QUICCredentialRotationPreservesSiblingAndLedger(t *testing.T) {
	echo, received := snellPacketEcho(t, "127.0.0.1")
	tcpTarget, tcpReceived := loopbackEchoTarget(t, "tcp")
	listen, siblingPort := port(t), port(t)
	config := snellNativeConfig(5, listen)
	config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "sibling", "listen": "127.0.0.1", "port": siblingPort, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": "tcp", "rewriteAddress": "127.0.0.1", "rewritePort": tcpTarget, "clientId": snellOwner}})
	instance := start(t, loopbackJSON(t, config))
	c := snellQUICClient(t, listen)
	raw := []byte{0x40, 0x77}
	snellQUICExchange(t, c, snellQUICEnvelope(t, snellPSK, echo.LocalAddr(), raw, "wire-id"), raw)
	sibling := loopbackFlow(t, "tcp", siblingPort)
	exchange(t, sibling, []byte("sibling"))
	manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
	handler, err := manager.GetHandler(context.Background(), "snell")
	if err != nil {
		t.Fatal(err)
	}
	if err = (&command.RemoveUserOperation{Email: "snell-owner"}).ApplyInbound(context.Background(), handler); err != nil {
		t.Fatal(err)
	}
	exchange(t, sibling, []byte("still-alive"))
	deadline := time.Now().Add(time.Second)
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	for {
		snap, _ := engine.Snapshot(snellOwner)
		if snap.ActiveSessions == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("removed QUIC retained a session or closed sibling: %+v", snap)
		}
		time.Sleep(time.Millisecond)
	}
	freshPSK := "native-quic-replacement-secret"
	operation := &command.AddUserOperation{User: &protocol.User{ClientId: snellOwner, Email: "snell-owner", Account: serial.ToTypedMessage(&coreSnell.Account{Psk: freshPSK})}}
	if err = operation.ApplyInbound(context.Background(), handler); err != nil {
		t.Fatal(err)
	}
	old := snellQUICClient(t, listen)
	old.SetDeadline(time.Now().Add(100 * time.Millisecond))
	old.Write(snellQUICEnvelope(t, snellPSK, echo.LocalAddr(), raw, "old-wire-id"))
	if _, err = old.Read(make([]byte, 65535)); err == nil {
		t.Fatal("old PSK established a new QUIC source")
	}
	fresh := snellQUICClient(t, listen)
	snellQUICExchange(t, fresh, snellQUICEnvelope(t, freshPSK, echo.LocalAddr(), raw, "fresh-wire-id"), raw)
	snap, err := engine.Snapshot(snellOwner)
	if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 22, RawDownload: 22, BilledBytes: 66}) || received.Load() != 4 || tcpReceived.Load() != 18 {
		t.Fatalf("QUIC owner rotation lost shared history: %+v udp=%d tcp=%d err=%v", snap, received.Load(), tcpReceived.Load(), err)
	}
}

// This structural Initial exercises datagram selection; it is not a QUIC
// handshake. Real QUIC and HTTP/3 peers are exercised separately below.
func snellQUICInitial(size int) []byte {
	p := bytes.Repeat([]byte{0x71}, size)
	copy(p, []byte{0xc0, 0, 0, 0, 1, 8, 1, 2, 3, 4, 5, 6, 7, 8, 8, 8, 7, 6, 5, 4, 3, 2, 1, 0})
	// Two-byte QUIC packet length includes packet number and encrypted payload.
	binary.BigEndian.PutUint16(p[24:26], 0x4000|uint16(size-26))
	return p
}

func TestNativeSnellV5QUICOutboundOfficialEnvelopeRawRepeatedSharedLedger(t *testing.T) {
	server := snellReferenceServer(t, 5, "", "", snellPSK)
	time.Sleep(100 * time.Millisecond)
	echo, received := snellPacketEcho(t, "127.0.0.1")
	listen := port(t)
	config := snellNativeConfig(5, port(t))
	config["inbounds"] = []any{map[string]any{"tag": "origin", "listen": "127.0.0.1", "port": listen, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": "udp", "rewriteAddress": "127.0.0.1", "rewritePort": echo.LocalAddr().(*net.UDPAddr).Port, "clientId": snellOwner, "email": "quic-reference-owner"}}}
	config["outbounds"] = []any{map[string]any{"protocol": "snell", "settings": map[string]any{"version": 5, "psk": snellPSK, "address": "127.0.0.1", "port": server}}}
	instance := start(t, loopbackJSON(t, config))
	flow := loopbackFlow(t, "udp", listen)
	initial := snellQUICInitial(4000)
	raw := append([]byte{0x40}, bytes.Repeat([]byte{0x72}, 511)...)
	for _, payload := range [][]byte{initial, raw, initial} {
		flow.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := flow.Write(payload); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 65535)
		n, err := flow.Read(b)
		if err != nil || !bytes.Equal(payload, b[:n]) {
			t.Fatalf("official QUIC outbound full packet changed: want=%d got=%d target=%d err=%v", len(payload), n, received.Load(), err)
		}
	}
	snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
	if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 8512, RawDownload: 8512, BilledBytes: 25536}) || received.Load() != 8512 {
		t.Fatalf("official QUIC outbound payload ledger changed: %+v target=%d err=%v", snap, received.Load(), err)
	}
}

// Test client framing follows pinned OpenSnell and the captured official wire.
// It remains a source-client test, distinct from actual Surge interoperability.
func snellQUICEnvelope(t *testing.T, psk string, target net.Addr, inner []byte, wireID string) []byte {
	t.Helper()
	p, err := snellQUICEnvelopeValue(psk, target, inner, wireID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func snellQUICEnvelopeValue(psk string, target net.Addr, inner []byte, wireID string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(target.String())
	if err != nil {
		return nil, err
	}
	port, err := net.LookupPort("udp", portText)
	if err != nil {
		return nil, err
	}
	payload := []byte{1, 1, byte(len(wireID))}
	payload = append(payload, wireID...)
	payload = append(payload, byte(len(host)))
	payload = append(payload, host...)
	payload = binary.BigEndian.AppendUint16(payload, uint16(port))
	payload = append(payload, inner...)
	salt := make([]byte, 16)
	if _, err = io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	salt[0] &^= 0x40
	aead, err := S.NewAEAD(S.DeriveKey([]byte(psk), salt))
	if err != nil {
		return nil, err
	}
	header := []byte{4, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(header[5:7], uint16(len(payload)))
	nonce := make([]byte, aead.NonceSize())
	packet := append(salt, aead.Seal(nil, nonce, header, nil)...)
	binary.LittleEndian.PutUint64(nonce, 1)
	return append(packet, aead.Seal(nil, nonce, payload, nil)...), nil
}

func TestNativeSnellV5QUICInboundEnvelopeRawRepeatedCanonicalLedger(t *testing.T) {
	echo, received := snellPacketEcho(t, "127.0.0.1")
	listen := port(t)
	instance := start(t, loopbackJSON(t, snellNativeConfig(5, listen)))
	client, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	initial := append([]byte{0xc0, 0, 0, 0, 1}, bytes.Repeat([]byte{0x71}, 3995)...)
	envelope := snellQUICEnvelope(t, snellPSK, echo.LocalAddr(), initial, "untrusted-wire-identity")
	raw := append([]byte{0x40}, bytes.Repeat([]byte{0x72}, 511)...)
	for _, packet := range [][]byte{envelope, raw, envelope} {
		expected := packet
		if bytes.Equal(packet, envelope) {
			expected = initial
		}
		client.SetDeadline(time.Now().Add(time.Second))
		if _, err = client.Write(packet); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 65535)
		n, err := client.Read(response)
		if err != nil || !bytes.Equal(response[:n], expected) {
			t.Fatalf("native v5 QUIC exact packet failed: length=%d target=%d err=%v", n, received.Load(), err)
		}
	}
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	snap, err := engine.Snapshot(snellOwner)
	if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 8512, RawDownload: 8512, BilledBytes: 25536}) || received.Load() != 8512 {
		t.Fatalf("QUIC envelope overhead or wire ID changed canonical ledger: %+v target=%d err=%v", snap, received.Load(), err)
	}
	wrong, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	wrong.Write(snellQUICEnvelope(t, "wrong-quic-test-secret", echo.LocalAddr(), initial, "pretended-other-owner"))
	time.Sleep(100 * time.Millisecond)
	if received.Load() != 8512 {
		t.Fatal("wrong QUIC PSK reached target")
	}
	policy, _, err := engine.GetClient(snellOwner)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = false
	policy.Version++
	if err = testauthority.Apply(t, engine, policy); err != nil {
		t.Fatal(err)
	}
	client.SetDeadline(time.Now().Add(150 * time.Millisecond))
	client.Write(raw)
	if _, err = client.Read(make([]byte, 65535)); err == nil {
		t.Fatal("disabled native QUIC association continued")
	}
	deadline := time.Now().Add(time.Second)
	for {
		snap, err = engine.Snapshot(snellOwner)
		if err != nil {
			t.Fatal(err)
		}
		if snap.ActiveSessions == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native QUIC cleanup leaked policy session")
		}
		time.Sleep(time.Millisecond)
	}
	if received.Load() != 8512 {
		t.Fatal("disabled native QUIC forwarded new payload")
	}
}
