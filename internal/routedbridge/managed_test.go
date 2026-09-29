package routedbridge

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
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestManagedBridgeAuthenticatesAndReturnsActualUDPPeer(t *testing.T) {
	binaryPath := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for actual managed routing")
	}
	address := netip.MustParseAddrPort(reserveDatagramCoreAddress(t))
	binding := ClientBinding{PolicyID: uuid.NewString(), Email: "managed-alice"}
	bridge, err := NewManaged("managed-inbound", address, []ClientBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	startManagedCore(t, binaryPath, bridge)
	if err := bridge.Check(t.Context()); err != nil {
		t.Fatalf("configured core did not authenticate readiness: %v", err)
	}
	wrong, err := NewManaged("managed-inbound", address, []ClientBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	if err := wrong.Check(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("different private credential passed readiness: %v", err)
	}
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	port := uint16(target.LocalAddr().(*net.UDPAddr).Port)
	conn, err := bridge.DialUDP(t.Context(), binding.PolicyID, netip.MustParseAddrPort("127.0.0.2:34567"), "localhost", port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, size := range []int{0, 1, 8193, 65507} {
		payload := bytes.Repeat([]byte{23}, size)
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if n, err := conn.Write(payload); err != nil || n != size {
			t.Fatalf("write %d-byte packet: %d, %v", size, n, err)
		}
		_ = target.SetReadDeadline(time.Now().Add(time.Second))
		buffer := make([]byte, 65535)
		n, peer, err := target.ReadFrom(buffer)
		if err != nil || n != size || !bytes.Equal(buffer[:n], payload) {
			t.Fatalf("target packet %d: got %d, %v", size, n, err)
		}
		if _, err := target.WriteTo(payload, peer); err != nil {
			t.Fatal(err)
		}
		n, responsePeer, err := conn.ReadFrom(buffer)
		if err != nil || n != size || !bytes.Equal(buffer[:n], payload) {
			t.Fatalf("reply packet %d: got %d, %v", size, n, err)
		}
		if responsePeer.String() != target.LocalAddr().String() {
			t.Fatalf("actual peer lost or invented: got %s, want %s", responsePeer, target.LocalAddr())
		}
	}
}

func TestManagedBridgeRejectsStockCoreBeforeAnyTarget(t *testing.T) {
	binaryPath := os.Getenv("XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XRAY_E2E_BINARY for stock core rejection")
	}
	address := netip.MustParseAddrPort(reserveDatagramCoreAddress(t))
	binding := ClientBinding{PolicyID: uuid.NewString(), Email: "stock-proof"}
	bridge, err := NewManaged("managed", address, []ClientBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	target, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	startDatagramCore(t, binaryPath, address.String(), bridge.clients[binding.PolicyID].password, func(cfg map[string]any) {
		inbound := cfg["inbounds"].([]any)[0].(map[string]any)
		inbound["streamSettings"] = map[string]any{"sockopt": map[string]any{"acceptProxyProtocol": true}}
		cfg["outbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["redirect"] = target.Addr().String()
	})
	if err := bridge.Check(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stock core passed capability authentication: %v", err)
	}
	conn, err := bridge.DialTCP(t.Context(), binding.PolicyID, netip.MustParseAddrPort("127.0.0.1:12345"), "localhost", uint16(target.Addr().(*net.TCPAddr).Port))
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stock core accepted a target request: %v", err)
	}
	_ = target.SetDeadline(time.Now().Add(80 * time.Millisecond))
	accepted, err := target.Accept()
	if accepted != nil {
		_ = accepted.Close()
		t.Fatal("capability probe created an outbound before authentication")
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("target accept failed for another reason: %v", err)
	}
}

func TestManagedBridgeCancellationClosesStalledPeer(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	binding := ClientBinding{PolicyID: uuid.NewString(), Email: "stalled"}
	bridge, err := NewManaged("managed", netip.MustParseAddrPort(l.Addr().String()), []ClientBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- bridge.Check(ctx) }()
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("canceled probe passed readiness: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled probe left handshake pending")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("cancellation left connection open: %v", err)
	}
}

func startManagedCore(t *testing.T, binaryPath string, bridge *ManagedBridge, configure ...func(map[string]any)) *exec.Cmd {
	t.Helper()
	return startDatagramCore(t, binaryPath, bridge.address.String(), "unused-fixture-password", func(base map[string]any) {
		base["inbounds"] = []any{}
		for _, apply := range configure {
			apply(base)
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
		clear(base)
		if err := json.Unmarshal(encoded, &base); err != nil {
			t.Fatal(err)
		}
	})
}
