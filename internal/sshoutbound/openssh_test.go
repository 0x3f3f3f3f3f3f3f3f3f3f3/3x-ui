//go:build linux

package sshoutbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func testOpenSSH(t *testing.T) Config {
	t.Helper()
	binary := os.Getenv("SSH_E2E_SERVER")
	if binary == "" {
		t.Skip("set SSH_E2E_SERVER to an OpenSSH sshd binary for real upstream interoperability")
	}
	if os.Geteuid() != 0 {
		t.Skip("the isolated OpenSSH fixture requires root for its privilege separation")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".3x-ui-ssh-upstream-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	hostPrivate, hostPublic := testKey(t)
	clientPrivate, clientPublic := testKey(t)
	for name, value := range map[string]string{"host-key": hostPrivate, "authorized_keys": clientPublic} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	_ = reservation.Close()
	configuration := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nAuthorizedKeysFile %s\nPidFile %s\nAllowUsers root\nPermitRootLogin prohibit-password\nAuthenticationMethods publickey\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM yes\nAllowTcpForwarding local\nAllowAgentForwarding no\nX11Forwarding no\nPermitTTY no\nMaxSessions 0\nLogLevel VERBOSE\n", port, filepath.Join(dir, "host-key"), filepath.Join(dir, "authorized_keys"), filepath.Join(dir, "sshd.pid"))
	configPath := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "sshd.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-D", "-e", "-f", configPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("isolated sshd diagnostics: %s", data)
		}
	})
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("isolated sshd did not listen: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return Config{Address: "127.0.0.1", Port: port, User: "root", PrivateKey: clientPrivate, HostKey: hostPublic}
}

func TestConnectorOpenSSHForwardingAndStrictHostPin(t *testing.T) {
	config := testOpenSSH(t)
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	var calls atomic.Int64
	received := make(chan int64, 1)
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			go func() {
				defer conn.Close()
				n, _ := io.Copy(conn, conn)
				received <- n
			}()
		}
	}()
	connector, err := NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connector.Close() })
	_, port, _ := net.SplitHostPort(target.Addr().String())
	conn, err := connector.DialContext(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("real OpenSSH forwarding did not connect: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("ssh upstream payload\n"), 2048)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(response, payload) {
		t.Fatalf("SSH payload/half-close changed: got %d bytes, want %d; err=%v", len(response), len(payload), err)
	}
	if got := <-received; got != int64(len(payload)) {
		t.Fatalf("independent target received %d bytes, want %d", got, len(payload))
	}
	for range 2 {
		if err := conn.Close(); err != nil {
			t.Fatalf("closing an owned SSH stream was not idempotent: %v", err)
		}
	}
	for _, kind := range []string{"host-pin", "client-key"} {
		t.Run(kind, func(t *testing.T) {
			bad := config
			private, public := testKey(t)
			if kind == "host-pin" {
				bad.HostKey = public
			} else {
				bad.PrivateKey = private
			}
			wrong, err := NewConnector(bad)
			if err != nil {
				t.Fatal(err)
			}
			defer wrong.Close()
			conn, err := wrong.DialContext(context.Background(), "tcp", target.Addr().String())
			if conn != nil {
				_ = conn.Close()
				t.Fatal("incorrect pin/identity opened a target connection")
			}
			if kind == "host-pin" && !errors.Is(err, ErrHostKeyMismatch) {
				t.Fatalf("wrong pin result=%v, want host-key mismatch", err)
			}
			if kind == "client-key" && (err == nil || !strings.Contains(err.Error(), "unable to authenticate")) {
				t.Fatalf("wrong client key result=%v, want signed-authentication rejection", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("rejected SSH identity reached target: %d calls", calls.Load())
			}
		})
	}
	t.Logf("real OpenSSH target observed exactly %d upload and echo bytes; wrong host pin and client key opened no target connection", len(payload))
}

func TestConnectorOpenSSHIPv6Target(t *testing.T) {
	config := testOpenSSH(t)
	target, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("IPv6 target fixture unavailable: %v", err)
	}
	defer target.Close()
	received := make(chan string, 1)
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		data, _ := io.ReadAll(conn)
		received <- string(data)
		_, _ = conn.Write([]byte("IPv6 response"))
	}()
	c, err := NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn, err := c.DialContext(context.Background(), "tcp", target.Addr().String())
	if err != nil {
		t.Fatalf("actual OpenSSH IPv6 destination failed: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("IPv6 request")); err != nil {
		t.Fatal(err)
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil || string(response) != "IPv6 response" {
		t.Fatalf("IPv6 response=%q err=%v", response, err)
	}
	select {
	case got := <-received:
		if got != "IPv6 request" {
			t.Fatalf("independent IPv6 target received %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("independent IPv6 target did not observe request")
	}
}
