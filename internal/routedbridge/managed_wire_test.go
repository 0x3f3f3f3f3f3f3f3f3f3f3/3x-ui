package routedbridge

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestManagedBridgeRejectsForgedAcknowledgementWithoutTarget(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	binding := ClientBinding{PolicyID: uuid.NewString(), Email: "forged"}
	bridge, err := NewManaged("managed", netip.MustParseAddrPort(l.Addr().String()), []ClientBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadString('\n'); err != nil {
			done <- err
			return
		}
		var probe [100]byte
		if _, err := io.ReadFull(reader, probe[:]); err != nil {
			done <- err
			return
		}
		var reply [36]byte
		copy(reply[:], "XUI\x01")
		if _, err := conn.Write(reply[:]); err != nil {
			done <- err
			return
		}
		_, err = reader.ReadByte()
		done <- err
	}()
	conn, err := bridge.DialTCP(t.Context(), binding.PolicyID, netip.MustParseAddrPort("127.0.0.2:34567"), "private-target.invalid", 443)
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("forged authenticated capability accepted: %v", err)
	}
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("target was sent before capability authentication: %v", err)
	}
}

func TestManagedCoreFragmentedAuthenticationAndFixedUDPRoute(t *testing.T) {
	binaryPath := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for fragmented managed core requests")
	}
	binding := ClientBinding{PolicyID: uuid.NewString(), Email: "fragmented"}
	bridge, err := NewManaged("managed", netip.MustParseAddrPort(reserveDatagramCoreAddress(t)), []ClientBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	startManagedCore(t, binaryPath, bridge)
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	other, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := net.DialTimeout("tcp", bridge.address.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	fragmented := &fragmentedManagedConn{Conn: conn}
	if err := bridge.authenticate(fragmented, bridge.clients[binding.PolicyID], netip.MustParseAddrPort("127.0.0.2:34567")); err != nil {
		t.Fatalf("fragmented authentication rejected: %v", err)
	}
	port := uint16(target.LocalAddr().(*net.UDPAddr).Port)
	address := binary.BigEndian.AppendUint16([]byte{1, 127, 0, 0, 1}, port)
	request := append([]byte{3}, address...)
	request = append(request, '\r', '\n')
	if _, err := fragmented.Write(request); err != nil {
		t.Fatal(err)
	}
	packet := &DatagramConn{Conn: conn, address: address}
	if _, err := packet.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	_ = target.SetReadDeadline(time.Now().Add(time.Second))
	p := make([]byte, 64)
	if n, _, err := target.ReadFrom(p); err != nil || string(p[:n]) != "first" {
		t.Fatalf("fragmented target stage did not dispatch: %q / %v", p[:n], err)
	}
	packet.address = binary.BigEndian.AppendUint16([]byte{1, 127, 0, 0, 1}, uint16(other.LocalAddr().(*net.UDPAddr).Port))
	if _, err := packet.Write([]byte("changed")); err != nil {
		t.Fatal(err)
	}
	_ = other.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var timeout net.Error
	if n, _, err := other.ReadFrom(p); !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("changed target bypassed original route selection: %q / %v", p[:n], err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := conn.Read(p); n != 0 || err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("changed target did not retire authenticated stream: %d / %v", n, err)
	}
}

type fragmentedManagedConn struct {
	net.Conn
	proxyHeaderSent bool
}

func (c *fragmentedManagedConn) Write(p []byte) (int, error) {
	if !c.proxyHeaderSent {
		c.proxyHeaderSent = true
		return c.Conn.Write(p)
	}
	for i, b := range p {
		if _, err := c.Conn.Write([]byte{b}); err != nil {
			return i, err
		}
		time.Sleep(time.Millisecond)
	}
	return len(p), nil
}

func TestManagedDatagramRejectsMalformedReplyAndClosesStream(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame []byte
		limit int
		want  error
	}{
		{"domain-peer", []byte{3, 1, 'x'}, 32, ErrUnavailable},
		{"missing-port", []byte{1, 127, 0, 0, 1, 0, 0, 0, 0, '\r', '\n'}, 32, ErrUnavailable},
		{"oversize", []byte{1, 127, 0, 0, 1, 0, 80, 255, 255, '\r', '\n'}, 65535, ErrUnavailable},
		{"delimiter", []byte{1, 127, 0, 0, 1, 0, 80, 0, 0, '\n', '\r'}, 32, ErrUnavailable},
		{"short-buffer", []byte{1, 127, 0, 0, 1, 0, 80, 0, 3, '\r', '\n', 1, 2, 3}, 2, io.ErrShortBuffer},
		{"partial-payload", []byte{1, 127, 0, 0, 1, 0, 80, 0, 3, '\r', '\n', 1}, 32, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = server.Write(tc.frame)
				_ = server.Close()
			}()
			_ = client.SetDeadline(time.Now().Add(time.Second))
			conn := &DatagramConn{Conn: client}
			n, peer, err := conn.ReadFrom(make([]byte, tc.limit))
			if n != 0 || peer != nil || !errors.Is(err, tc.want) {
				t.Fatalf("malformed reply reached consumer: n=%d peer=%v err=%v; want %v", n, peer, err, tc.want)
			}
			<-done
			if _, err := client.Write([]byte{1}); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("failed frame left stream usable: %v", err)
			}
		})
	}
}

func TestManagedCoreExitClosesExistingPacketFlowAndRefusesNewTarget(t *testing.T) {
	binaryPath := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for managed core exit")
	}
	binding := ClientBinding{PolicyID: uuid.NewString(), Email: "exit-proof"}
	bridge, err := NewManaged("managed", netip.MustParseAddrPort(reserveDatagramCoreAddress(t)), []ClientBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	core := startManagedCore(t, binaryPath, bridge)
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	port := uint16(target.LocalAddr().(*net.UDPAddr).Port)
	probeManagedUDPRoute(t, bridge, binding, netip.MustParseAddrPort("127.0.0.2:34567"), "localhost", port, target, "live", "127.0.0.1")
	conn, err := bridge.DialUDP(t.Context(), binding.PolicyID, netip.MustParseAddrPort("127.0.0.2:34567"), "localhost", port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("open")); err != nil {
		t.Fatal(err)
	}
	_ = target.SetReadDeadline(time.Now().Add(time.Second))
	p := make([]byte, 64)
	if n, _, err := target.ReadFrom(p); err != nil || string(p[:n]) != "open" {
		t.Fatalf("existing packet flow never reached target: %q / %v", p[:n], err)
	}
	if err := core.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = core.Wait()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, peer, err := conn.ReadFrom(p)
	var timeout net.Error
	if n != 0 || peer != nil || err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("dead core left an existing packet stream usable: n=%d peer=%v err=%v", n, peer, err)
	}
	if err := bridge.Check(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dead core passed readiness: %v", err)
	}
	if other, err := bridge.DialUDP(t.Context(), binding.PolicyID, netip.MustParseAddrPort("127.0.0.2:34567"), "localhost", port); other != nil || !errors.Is(err, ErrUnavailable) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("dead core permitted a new target: %v", err)
	}
}
