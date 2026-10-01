package snell

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	X "github.com/xtls/xray-core/common/net"
)

func TestNativeSnellV5QUICCodecOfficialEnvelopeRawAndRepeated(t *testing.T) {
	dir := os.Getenv("SNELL_REFERENCE_DIR")
	if dir == "" {
		t.Skip("pinned official ARM64 fixture required")
	}
	fixture := filepath.Join(dir, "v5.0.1", "snell-server")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "c9e1cc1f1a86e7d2958f2bc41ff9dc668edf479455a651ea05c6db2c18cd2e4e" {
		t.Fatal("official v5 fixture provenance mismatch")
	}
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := reserve.Addr().String()
	reserve.Close()
	psk := []byte("native-quic-official-test-secret")
	cfg := filepath.Join(t.TempDir(), "snell.conf")
	if err = os.WriteFile(cfg, []byte(fmt.Sprintf("[snell-server]\nlisten = %s\npsk = %s\nversion = 5\nipv6 = true\n", listen, psk)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, fixture, "-c", cfg)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	ready := false
	for range 100 {
		c, e := net.DialTimeout("tcp", listen, 10*time.Millisecond)
		if e == nil {
			c.Close()
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("official fixture did not start")
	}
	time.Sleep(100 * time.Millisecond)
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	packets := make(chan []byte, 8)
	go func() {
		b := make([]byte, 65535)
		for {
			n, a, e := echo.ReadFrom(b)
			if e != nil {
				return
			}
			p := append([]byte(nil), b[:n]...)
			packets <- p
			_, _ = echo.WriteTo(p, a)
		}
	}()
	_, initial, err := decodeQUICEnvelope([]byte("JFdZLmZLtYtUP308QrqdoA=="), realCapturedEnvelopePkt1)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := encodeQUICEnvelope(psk, X.DestinationFromAddr(echo.LocalAddr()), initial)
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("udp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	raw := append([]byte{0x40}, bytes.Repeat([]byte{0x71}, 1279)...)
	for _, packet := range [][]byte{envelope, raw, envelope} {
		expected := packet
		if bytes.Equal(packet, envelope) {
			expected = initial
		}
		client.SetDeadline(time.Now().Add(time.Second))
		if _, err = client.Write(packet); err != nil {
			t.Fatal(err)
		}
		select {
		case actual := <-packets:
			if !bytes.Equal(expected, actual) {
				t.Fatalf("official QUIC target received%d want%d", len(actual), len(expected))
			}
		case <-time.After(time.Second):
			t.Fatal("official QUIC target did not receive complete packet")
		}
		b := make([]byte, 65535)
		n, e := client.Read(b)
		if e != nil || !bytes.Equal(expected, b[:n]) {
			t.Fatalf("official QUIC raw reply changed: length=%d err=%v", n, e)
		}
	}
	// The first authenticated envelope fixes this source's target. A later
	// authenticated envelope is unwrapped, but its different target is ignored.
	second, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	rebound, err := encodeQUICEnvelope(psk, X.DestinationFromAddr(second.LocalAddr()), initial)
	if err != nil {
		t.Fatal(err)
	}
	for _, packet := range [][]byte{rebound, raw} {
		expected := initial
		if bytes.Equal(packet, raw) {
			expected = raw
		}
		client.SetDeadline(time.Now().Add(time.Second))
		if _, err = client.Write(packet); err != nil {
			t.Fatal(err)
		}
		select {
		case actual := <-packets:
			if !bytes.Equal(expected, actual) {
				t.Fatalf("official QUIC original target received%d want%d", len(actual), len(expected))
			}
		case <-time.After(time.Second):
			t.Fatal("official QUIC original source target did not receive packet")
		}
		b := make([]byte, 65535)
		n, e := client.Read(b)
		if e != nil || !bytes.Equal(expected, b[:n]) {
			t.Fatalf("official QUIC rebound raw reply changed: length=%d err=%v", n, e)
		}
	}
	second.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err = second.ReadFrom(make([]byte, 65535)); err == nil {
		t.Fatal("official QUIC source association unexpectedly changed target")
	}
	wrong, err := net.Dial("udp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	bad, _ := encodeQUICEnvelope([]byte("wrong-quic-test-secret"), X.DestinationFromAddr(echo.LocalAddr()), initial)
	wrong.Write(bad)
	select {
	case <-packets:
		t.Fatal("official QUIC wrong PSK reached target")
	case <-time.After(100 * time.Millisecond):
	}
	v6, err := net.ListenPacket("udp", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer v6.Close()
	client6, err := net.Dial("udp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer client6.Close()
	envelope6, err := encodeQUICEnvelope(psk, X.DestinationFromAddr(v6.LocalAddr()), initial)
	if err != nil {
		t.Fatal(err)
	}
	client6.SetDeadline(time.Now().Add(time.Second))
	if _, err = client6.Write(envelope6); err != nil {
		t.Fatal(err)
	}
	v6.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 65535)
	n, addr, err := v6.ReadFrom(b)
	if err != nil || !bytes.Equal(initial, b[:n]) {
		t.Fatalf("official QUIC IPv6 target changed: length=%d err=%v", n, err)
	}
	if _, err = v6.WriteTo(b[:n], addr); err != nil {
		t.Fatal(err)
	}
	n, err = client6.Read(b)
	if err != nil || !bytes.Equal(initial, b[:n]) {
		t.Fatalf("official QUIC IPv6 raw reply changed: length=%d err=%v", n, err)
	}
}
