package policy_test

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
)

func TestTunnelFirstLargeUDPDatagramPreservesTargetAndLedger(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, size := range []int{13000, 65507} {
			t.Run(fmt.Sprintf("managed-%t/%d", managed, size), func(t *testing.T) {
				testTunnelDatagram(t, managed, bytes.Repeat([]byte{0x83}, size))
			})
		}
	}
}

func TestTunnelFirstEmptyUDPDatagramPreservesTargetAndLedger(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed-%t", managed), func(t *testing.T) {
			testTunnelDatagram(t, managed, []byte{})
		})
	}
}

func testTunnelDatagram(t *testing.T, managed bool, payload []byte) {
	t.Helper()
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	received := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 65535)
		n, peer, err := target.ReadFrom(buffer)
		if err != nil {
			return
		}
		received <- append([]byte(nil), buffer[:n]...)
		_, _ = target.WriteTo(buffer[:n], peer)
	}()
	listen := port(t)
	config := loopbackTunnelConfig(0, "udp", false, listen, target.LocalAddr().(*net.UDPAddr).Port)
	if !managed {
		delete(config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any), "clientId")
	}
	instance := start(t, loopbackJSON(t, config))
	flow := loopbackFlow(t, "udp", listen)
	if _, err := flow.Write(payload); err != nil {
		t.Fatal(err)
	}
	select {
	case actual := <-received:
		if !bytes.Equal(actual, payload) {
			t.Fatalf("Tunnel target received a truncated datagram: got=%d want=%d", len(actual), len(payload))
		}
	case <-time.After(2 * time.Second):
		if len(payload) == 0 {
			_ = flow.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := flow.Read(make([]byte, 1))
			t.Logf("target missing empty datagram; source read n=%d err=%v", n, err)
		}
		t.Fatal("Tunnel target did not receive the first datagram")
	}
	_ = flow.SetReadDeadline(time.Now().Add(2 * time.Second))
	reply := make([]byte, 65535)
	if n, err := flow.Read(reply); err != nil || !bytes.Equal(reply[:n], payload) {
		t.Fatalf("Tunnel reply lost datagram boundaries: n=%d err=%v", n, err)
	}
	if managed {
		engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
		state, err := engine.Snapshot("owner")
		if err != nil || state.Usage != (clientpolicy.Usage{RawUpload: uint64(len(payload)), RawDownload: uint64(len(payload)), BilledBytes: uint64(len(payload) * 3)}) {
			t.Fatalf("full UDP payload did not reach shared billing: %+v %v", state, err)
		}
	}
}
