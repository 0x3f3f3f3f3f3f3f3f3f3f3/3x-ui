package routedbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCoreTrojanPreservesWholeDatagrams(t *testing.T) {
	binaryPath := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binaryPath == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for actual authenticated core UDP boundary tests")
	}
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	address := reserveDatagramCoreAddress(t)
	password := uuid.NewString()
	startDatagramCore(t, binaryPath, address, password)
	port := uint16(target.LocalAddr().(*net.UDPAddr).Port)
	for _, size := range []int{1, 0, 8170, 8192, 8193, 65507} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			conn := connectDatagramCore(t, address, password, port)
			defer conn.Close()
			payload := bytes.Repeat([]byte{byte(size%251 + 1)}, size)
			request := []byte{3, 9, 'l', 'o', 'c', 'a', 'l', 'h', 'o', 's', 't'}
			request = binary.BigEndian.AppendUint16(request, port)
			request = binary.BigEndian.AppendUint16(request, uint16(size))
			request = append(request, '\r', '\n')
			request = append(request, payload...)
			if _, err := conn.Write(request); err != nil {
				t.Fatal(err)
			}
			_ = target.SetReadDeadline(time.Now().Add(time.Second))
			received := make([]byte, 65536)
			n, peer, err := target.ReadFrom(received)
			if err != nil || n != size || !bytes.Equal(received[:n], payload) {
				t.Fatalf("core changed or lost upload datagram: size=%d got=%d error=%v", size, n, err)
			}
			if n, err := target.WriteTo(payload, peer); err != nil || n != size {
				t.Fatalf("target response: n=%d error=%v", n, err)
			}
			host, responsePort, response, err := readDatagramCoreReply(conn)
			if err != nil || responsePort != port || host != "localhost" || !bytes.Equal(response, payload) {
				t.Fatalf("core changed or lost reply datagram: size=%d got=%d source=%s:%d error=%v", size, len(response), host, responsePort, err)
			}
			_ = target.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
			var timeout net.Error
			if n, _, err := target.ReadFrom(received); err == nil {
				t.Fatalf("core split or duplicated upload into an additional %d-byte datagram", n)
			} else if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatal(err)
			}
		})
	}
}

func reserveDatagramCoreAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func startDatagramCore(t *testing.T, binaryPath, address, password string, configure ...func(map[string]any)) {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	portNumber, _ := strconv.Atoi(port)
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "managed-datagram-proof", "listen": "127.0.0.1", "port": portNumber, "protocol": "trojan",
			"settings": map[string]any{"clients": []any{map[string]any{"password": password, "email": "datagram-proof"}}},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.1"}}}}, "streamSettings": map[string]any{"sockopt": map[string]any{"domainStrategy": "UseIPv4"}}}},
		"dns":       map[string]any{"hosts": map[string]any{"localhost": "127.0.0.1"}},
	}
	for _, apply := range configure {
		apply(cfg)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "core.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binaryPath, "run", "-config", configPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
		if t.Failed() {
			output, _ := os.ReadFile(logPath)
			t.Logf("core output: %s", output)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned core listener did not become ready")
}

func connectDatagramCore(t *testing.T, address, password string, port uint16) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	hash := sha256.Sum224([]byte(password))
	header := []byte(hex.EncodeToString(hash[:]))
	header = append(header, '\r', '\n', 3, 1, 127, 0, 0, 1)
	header = binary.BigEndian.AppendUint16(header, port)
	header = append(header, '\r', '\n')
	if _, err := conn.Write(header); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	return conn
}

func readDatagramCoreReply(r io.Reader) (string, uint16, []byte, error) {
	var prefix [2]byte
	if _, err := io.ReadFull(r, prefix[:1]); err != nil {
		return "", 0, nil, err
	}
	kind := prefix[0]
	length := 0
	switch kind {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		if _, err := io.ReadFull(r, prefix[1:]); err != nil {
			return "", 0, nil, err
		}
		length = int(prefix[1])
	default:
		return "", 0, nil, fmt.Errorf("unknown response address type %d", kind)
	}
	address := make([]byte, length)
	if _, err := io.ReadFull(r, address); err != nil {
		return "", 0, nil, err
	}
	host := string(address)
	if kind != 3 {
		host = net.IP(address).String()
	}
	var suffix [6]byte
	if _, err := io.ReadFull(r, suffix[:]); err != nil {
		return "", 0, nil, err
	}
	if suffix[4] != '\r' || suffix[5] != '\n' {
		return "", 0, nil, fmt.Errorf("invalid packet delimiter")
	}
	payload := make([]byte, binary.BigEndian.Uint16(suffix[2:4]))
	_, err := io.ReadFull(r, payload)
	return host, binary.BigEndian.Uint16(suffix[:2]), payload, err
}
