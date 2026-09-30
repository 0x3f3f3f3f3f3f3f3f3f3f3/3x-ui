package policy_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/features/inbound"
)

type passwordUDPAssociation struct {
	control net.Conn
	packet  net.PacketConn
	relay   *net.UDPAddr
}

func passwordUDPAssociate(t *testing.T, listen int, username, password string) *passwordUDPAssociation {
	t.Helper()
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packet.Close() })
	control, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", listen), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	if err := control.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(control, response); err != nil || !bytes.Equal(response, []byte{5, 2}) {
		t.Fatalf("SOCKS5 method negotiation: %x %v", response, err)
	}
	auth := append([]byte{1, byte(len(username))}, []byte(username)...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, []byte(password)...)
	if _, err := control.Write(auth); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(control, response); err != nil || !bytes.Equal(response, []byte{1, 0}) {
		t.Fatalf("SOCKS5 user/password negotiation: %x %v", response, err)
	}
	source := packet.LocalAddr().(*net.UDPAddr).Port
	if _, err := control.Write([]byte{5, 3, 0, 1, 127, 0, 0, 1, byte(source >> 8), byte(source)}); err != nil {
		t.Fatal(err)
	}
	association := make([]byte, 10)
	if _, err := io.ReadFull(control, association); err != nil || !bytes.Equal(association[:4], []byte{5, 0, 0, 1}) {
		t.Fatalf("SOCKS5 UDP association: %x %v", association, err)
	}
	if err := control.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	return &passwordUDPAssociation{
		control: control, packet: packet,
		relay: &net.UDPAddr{IP: net.IP(association[4:8]), Port: int(association[8])<<8 | int(association[9])},
	}
}

func passwordUDPExchange(t *testing.T, association *passwordUDPAssociation, target int, payload string) {
	t.Helper()
	frame := append([]byte{0, 0, 0, 1, 127, 0, 0, 1, byte(target >> 8), byte(target)}, []byte(payload)...)
	if err := association.packet.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := association.packet.WriteTo(frame, association.relay); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 128)
	n, _, err := association.packet.ReadFrom(response)
	if err != nil || n < 10 || !bytes.Equal(response[:4], []byte{0, 0, 0, 1}) || string(response[10:n]) != payload {
		t.Fatalf("SOCKS5 UDP echo: n=%d response=%x error=%v", n, response[:n], err)
	}
}

func TestPasswordProxiesUDPIdentityAndCredentialCleanup(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprintf("active=%t", active), func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "udp")
			authPort := port(t)
			config := passwordProxyConfig(t, "mixed", authPort, port(t), target)
			settings := config["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)
			settings["accounts"] = append(settings["accounts"].([]any), map[string]any{"user": "other-user", "pass": "other-password", "clientId": "other", "email": "other-account"})
			policies := config["clientPolicy"].(map[string]any)
			policies["policies"] = append(policies["policies"].([]any), map[string]any{"clientId": "other", "version": 1, "enabled": true, "multiplierMicros": 1500000, "burstBytes": 65536})
			instance := start(t, loopbackJSON(t, config))
			owner := passwordUDPAssociate(t, authPort, "authenticated-user", "test-password")
			other := passwordUDPAssociate(t, authPort, "other-user", "other-password")
			ownerBytes := uint64(0)
			if active {
				passwordUDPExchange(t, owner, target, "owner!")
				ownerBytes = 6
			}
			passwordUDPExchange(t, other, target, "other!")
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			snapshot, err := engine.Snapshot("owner")
			if err != nil || snapshot.Usage != (clientpolicy.Usage{RawUpload: ownerBytes, RawDownload: ownerBytes, BilledBytes: ownerBytes * 3}) {
				t.Fatalf("UDP account lost its authenticated owner: %+v %v", snapshot, err)
			}
			manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
			handler, err := manager.GetHandler(context.Background(), "password")
			if err != nil {
				t.Fatal(err)
			}
			if err := (&command.RemoveUserOperation{Email: "canonical-account"}).ApplyInbound(context.Background(), handler); err != nil {
				t.Fatal(err)
			}
			assertPasswordProxyClosed(t, owner.control)
			deadline := time.Now().Add(time.Second)
			for {
				rebound, err := net.ListenPacket("udp4", owner.relay.String())
				if err == nil {
					_ = rebound.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("removed UDP association retained its listener: %v", err)
				}
				time.Sleep(time.Millisecond)
			}
			passwordUDPExchange(t, other, target, "alive!")
			snapshot, err = engine.Snapshot("other")
			if err != nil || snapshot.Usage != (clientpolicy.Usage{RawUpload: 12, RawDownload: 12, BilledBytes: 36}) || received.Load() != int64(ownerBytes+12) {
				t.Fatalf("same-IP sibling identity changed: %+v error=%v target=%d", snapshot, err, received.Load())
			}
		})
	}
}
