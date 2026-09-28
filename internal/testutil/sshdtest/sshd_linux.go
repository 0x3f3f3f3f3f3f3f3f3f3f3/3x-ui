//go:build linux

package sshdtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type Endpoint struct {
	Address    string
	Port       int
	User       string
	PrivateKey string
	HostKey    string
	logPath    string
	Stop       func()
}

func (e Endpoint) AuthenticatedConnections(t testing.TB) int {
	t.Helper()
	data, err := os.ReadFile(e.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(data, []byte("Accepted publickey for "))
}

func newKey(t testing.TB) (string, string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "isolated SSH upstream test")
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), string(ssh.MarshalAuthorizedKey(sshPub))
}

func Start(t testing.TB) Endpoint {
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
	hostPrivate, hostPublic := newKey(t)
	clientPrivate, clientPublic := newKey(t)
	for name, value := range map[string]string{"host-key": hostPrivate, "authorized_keys": clientPublic} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reservation, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
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
	cmd := exec.CommandContext(t.Context(), binary, "-D", "-e", "-f", configPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			_ = log.Close()
		})
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("isolated sshd diagnostics: %s", data)
		}
	})
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := (&net.Dialer{Timeout: 50 * time.Millisecond}).DialContext(t.Context(), "tcp", address)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("isolated sshd did not listen: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return Endpoint{Address: "127.0.0.1", Port: port, User: "root", PrivateKey: clientPrivate, HostKey: hostPublic, logPath: logPath, Stop: stop}
}
