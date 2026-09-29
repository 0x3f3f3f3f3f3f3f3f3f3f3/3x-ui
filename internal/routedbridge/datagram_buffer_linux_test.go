package routedbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func TestCoreUDPUsesFiniteClientBufferDespiteUnlimitedGlobalDefault(t *testing.T) {
	binaryPath := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for actual core UDP backpressure tests")
	}
	t.Setenv("xray.ray.buffer.size", "0")
	for _, limit := range []int{-1, 0} {
		name := "unlimited-control"
		if limit == 0 {
			name = "bounded-client"
		}
		t.Run(name, func(t *testing.T) {
			upstream, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer upstream.Close()
			address, password := reserveDatagramCoreAddress(t), uuid.NewString()
			startDatagramCore(t, binaryPath, address, password, func(cfg map[string]any) {
				cfg["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{"bufferSize": limit}}}
				cfg["inbounds"].([]any)[0].(map[string]any)["streamSettings"] = datagramSocketBuffer(unix.SO_RCVBUF)
				cfg["outbounds"] = []any{map[string]any{"protocol": "trojan", "streamSettings": datagramSocketBuffer(unix.SO_SNDBUF), "settings": map[string]any{"servers": []any{map[string]any{
					"address": "127.0.0.1", "port": upstream.Addr().(*net.TCPAddr).Port, "password": uuid.NewString(),
				}}}}}
			})
			conn := connectDatagramCore(t, address, password, 12345)
			defer conn.Close()
			if err := conn.(*net.TCPConn).SetWriteBuffer(65536); err != nil {
				t.Fatal(err)
			}
			frame := []byte{1, 127, 0, 0, 1, 0x30, 0x39}
			frame = binary.BigEndian.AppendUint16(frame, 4096)
			frame = append(frame, '\r', '\n')
			frame = append(frame, bytes.Repeat([]byte{21}, 4096)...)
			if _, err := conn.Write(frame); err != nil {
				t.Fatal(err)
			}
			_ = upstream.SetDeadline(time.Now().Add(3 * time.Second))
			stalled, err := upstream.AcceptTCP()
			if err != nil {
				t.Fatal(err)
			}
			defer stalled.Close()
			if err := stalled.SetReadBuffer(4096); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			sent := 0
			var writeErr error
			for range 8192 {
				n, err := conn.Write(frame)
				sent += n
				if err != nil {
					writeErr = err
					break
				}
			}
			if limit < 0 {
				if writeErr != nil || sent != 8192*4107 {
					t.Fatalf("unlimited control cannot establish the queue baseline: sent=%d error=%v", sent, writeErr)
				}
			} else {
				var timeout net.Error
				if !errors.As(writeErr, &timeout) || !timeout.Timeout() || sent > 8<<20 {
					t.Fatalf("finite UDP client policy did not backpressure a stalled outbound: sent=%d error=%v", sent, writeErr)
				}
			}
			_ = stalled.SetReadDeadline(time.Now().Add(time.Second))
			var header [59]byte
			if _, err := io.ReadFull(stalled, header[:]); err != nil || header[58] != 3 {
				t.Fatalf("test did not use the real core UDP outbound: command=%d error=%v", header[58], err)
			}
			t.Logf("accepted %d framed bytes before outbound release", sent)
		})
	}
}

func datagramSocketBuffer(option int) map[string]any {
	return map[string]any{"sockopt": map[string]any{"customSockopt": []any{map[string]any{
		"system": "linux", "network": "tcp", "level": strconv.Itoa(unix.SOL_SOCKET),
		"opt": strconv.Itoa(option), "type": "int", "value": "65536",
	}}}}
}
