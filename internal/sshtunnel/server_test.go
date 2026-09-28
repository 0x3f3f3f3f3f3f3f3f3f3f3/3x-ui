package sshtunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func tunnelDB(t *testing.T) (*gorm.DB, *database.ClientUsageLedger, *policyflow.Controller, model.ClientRecord) {
	t.Helper()
	return tunnelPolicyDB(t, 2000)
}

func tcpTestDial(ctx context.Context, destination Destination) (io.ReadWriteCloser, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port))))
}

func tunnelPolicyDB(t *testing.T, multiplier clientpolicy.Multiplier) (*gorm.DB, *database.ClientUsageLedger, *policyflow.Controller, model.ClientRecord) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tunnel.db")+"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=1000&_txlock=immediate"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []any{&model.ClientRecord{}, &xray.ClientTraffic{}, &model.ClientUsageAccount{}, &model.ClientUsageMeter{}} {
		if err := db.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	handle, _ := db.DB()
	t.Cleanup(func() { _ = handle.Close() })
	client := model.ClientRecord{Email: uuid.NewString(), Enable: true}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: client.Email, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	ledger := database.NewClientUsageLedger(db)
	if _, err := ledger.ChangeMultiplier(context.Background(), client.PolicyID, 1, multiplier, nil); err != nil {
		t.Fatal(err)
	}
	controller := policyflow.NewController(ledger, "node-a/shared-dispatch")
	t.Cleanup(controller.Close)
	if err := controller.Configure(context.Background(), client.PolicyID, policyflow.Rates{}); err != nil {
		t.Fatal(err)
	}
	return db, ledger, controller, client
}

func tunnelKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "isolated SSH test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return signer, path
}

func tunnelEcho(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				_, _ = io.Copy(conn, conn)
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = l.Close(); wg.Wait() })
	return l.Addr().String()
}

func tunnelServe(t *testing.T, server *Server) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	t.Cleanup(func() { server.Close(); <-done })
	return l.Addr().String()
}

func tunnelLocalAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	return address
}

type sshProcess struct {
	done   chan struct{}
	err    error
	output bytes.Buffer
}

func tunnelOpenSSH(t *testing.T, server, keyPath string, hostKey ssh.PublicKey, extra ...string) *sshProcess {
	t.Helper()
	return tunnelOpenSSHAs(t, "tunnel-user", server, keyPath, hostKey, extra...)
}

func tunnelOpenSSHAs(t *testing.T, username, server, keyPath string, hostKey ssh.PublicKey, extra ...string) *sshProcess {
	t.Helper()
	_, port, err := net.SplitHostPort(server)
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	line := fmt.Sprintf("[127.0.0.1]:%s %s", port, ssh.MarshalAuthorizedKey(hostKey))
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + knownHosts, "-o", "ExitOnForwardFailure=yes", "-o", "ConnectTimeout=3", "-i", keyPath, "-p", port}
	args = append(args, extra...)
	args = append(args, username+"@127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ssh", args...)
	process := &sshProcess{done: make(chan struct{})}
	cmd.Stderr = &process.output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() { process.err = cmd.Wait(); close(process.done) }()
	t.Cleanup(func() {
		cancel()
		<-process.done
		if t.Failed() {
			t.Logf("OpenSSH diagnostics: %s", process.output.String())
		}
	})
	return process
}

func tunnelWaitListener(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("OpenSSH did not expose its requested listener at %s", address)
}

func TestOpenSSHLocalAndDynamicForwardingShareAuthenticatedPolicy(t *testing.T) {
	db, ledger, controller, client := tunnelDB(t)
	hostKey, _ := tunnelKey(t)
	userKey, keyPath := tunnelKey(t)
	target := tunnelEcho(t)
	_, portText, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	destinations := make(chan Destination, 8)
	server, err := NewServer(Config{
		InboundTag: "ssh-test", HostKey: hostKey,
		Clients: []Client{{PolicyID: client.PolicyID, Username: "tunnel-user", PublicKeys: []ssh.PublicKey{userKey.PublicKey()}, Targets: []TargetRule{{Host: "localhost", Port: uint16(port)}}}},
	}, controller, func(ctx context.Context, target Destination) (io.ReadWriteCloser, error) {
		destinations <- target
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
	})
	if err != nil {
		t.Fatal(err)
	}
	address := tunnelServe(t, server)
	local, socks := tunnelLocalAddress(t), tunnelLocalAddress(t)
	localProcess := tunnelOpenSSH(t, address, keyPath, hostKey.PublicKey(), "-N", "-L", local+":localhost:"+portText)
	dynamicProcess := tunnelOpenSSH(t, address, keyPath, hostKey.PublicKey(), "-N", "-D", socks)
	tunnelWaitListener(t, local)
	tunnelWaitListener(t, socks)
	socksDialer, err := proxy.SOCKS5("tcp", socks, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("authenticated-payload"), 2048)
	var wg sync.WaitGroup
	for n := range 4 {
		wg.Go(func() {
			var conn net.Conn
			var err error
			if n%2 == 0 {
				conn, err = net.Dial("tcp", local)
			} else {
				conn, err = socksDialer.Dial("tcp", "localhost:"+portText)
			}
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(payload); err != nil {
				t.Error(err)
				return
			}
			reply := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
				t.Errorf("SSH tunnel changed or lost payload: %v", err)
			}
		})
	}
	wg.Wait()
	account, err := ledger.Read(context.Background(), client.PolicyID)
	expected := int64(4 * len(payload))
	if err != nil || account.Up != expected || account.Down != expected || account.Billed != 4*expected {
		t.Fatalf("SSH connections/channels did not share exact 2x billing: %+v, %v", account, err)
	}
	if len(destinations) < 4 {
		t.Fatalf("routing adapter was bypassed: only %d destinations for 4 successful channels", len(destinations))
	}
	for len(destinations) > 0 {
		destination := <-destinations
		if destination.PolicyID != client.PolicyID || destination.InboundTag != "ssh-test" || destination.Host != "localhost" || destination.Port != uint16(port) {
			t.Fatalf("routing lost authenticated identity or original destination: %+v", destination)
		}
	}
	if err := db.Model(&client).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(1250 * time.Millisecond)
	defer deadline.Stop()
	for _, process := range []*sshProcess{localProcess, dynamicProcess} {
		select {
		case <-process.done:
		case <-deadline.C:
			t.Fatal("disabled client retained an authenticated OpenSSH transport after 1.25s")
		}
	}
}

func TestAuthenticatedSSHRejectsUnrelatedCapabilitiesAndBoundsChannels(t *testing.T) {
	_, _, controller, client := tunnelDB(t)
	hostKey, _ := tunnelKey(t)
	userKey, _ := tunnelKey(t)
	target := tunnelEcho(t)
	_, portText, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	server, err := NewServer(Config{
		InboundTag: "ssh-restricted", HostKey: hostKey,
		Clients: []Client{{PolicyID: client.PolicyID, Username: "tunnel-user", PublicKeys: []ssh.PublicKey{userKey.PublicKey()}, Targets: []TargetRule{{Host: "127.0.0.1", Port: uint16(port)}}}},
	}, controller, func(ctx context.Context, target Destination) (io.ReadWriteCloser, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
	})
	if err != nil {
		t.Fatal(err)
	}
	address := tunnelServe(t, server)
	sshClient, err := ssh.Dial("tcp", address, &ssh.ClientConfig{User: "tunnel-user", Auth: []ssh.AuthMethod{ssh.PublicKeys(userKey)}, HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer sshClient.Close()
	payload := ssh.Marshal(struct {
		Host       string
		Port       uint32
		Origin     string
		OriginPort uint32
	}{"127.0.0.1", uint32(port), "127.0.0.1", 12345})
	for _, kind := range []string{"session", "auth-agent@openssh.com", "x11", "forwarded-tcpip"} {
		channel, _, err := sshClient.OpenChannel(kind, payload)
		if channel != nil {
			_ = channel.Close()
		}
		var refusal *ssh.OpenChannelError
		if !errors.As(err, &refusal) || refusal.Reason != ssh.Prohibited {
			t.Fatalf("authenticated client obtained forbidden %s capability: %v", kind, err)
		}
	}
	accepted, _, err := sshClient.SendRequest("tcpip-forward", true, ssh.Marshal(struct {
		Address string
		Port    uint32
	}{"127.0.0.1", 0}))
	if err != nil || accepted {
		t.Fatalf("remote forwarding was not denied by default: accepted=%v, err=%v", accepted, err)
	}
	for _, forbidden := range []string{"localhost:" + portText, "127.0.0.1:1"} {
		conn, err := sshClient.Dial("tcp", forbidden)
		if conn != nil {
			_ = conn.Close()
		}
		var refusal *ssh.OpenChannelError
		if !errors.As(err, &refusal) || refusal.Reason != ssh.Prohibited {
			t.Fatalf("target allowlist permitted %s: %v", forbidden, err)
		}
	}
	for range 64 {
		conn, err := sshClient.Dial("tcp", target)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		var one [1]byte
		if _, err := io.ReadFull(conn, one[:]); err != nil || one[0] != 'x' {
			t.Fatalf("authorized channel failed before reaching its capacity: %v", err)
		}
	}
	conn, err := sshClient.Dial("tcp", target)
	if conn != nil {
		_ = conn.Close()
	}
	var refusal *ssh.OpenChannelError
	if !errors.As(err, &refusal) || refusal.Reason != ssh.ResourceShortage {
		t.Fatalf("one transport could allocate more than 64 forwarding channels: %v", err)
	}
}

func TestOpenSSHRejectsUnknownClientKeyAndUnexpectedHostKey(t *testing.T) {
	_, _, controller, client := tunnelDB(t)
	hostKey, _ := tunnelKey(t)
	userKey, keyPath := tunnelKey(t)
	wrongKey, wrongPath := tunnelKey(t)
	server, err := NewServer(Config{
		InboundTag: "ssh-auth", HostKey: hostKey,
		Clients: []Client{{PolicyID: client.PolicyID, Username: "tunnel-user", PublicKeys: []ssh.PublicKey{userKey.PublicKey()}}},
	}, controller, func(ctx context.Context, target Destination) (io.ReadWriteCloser, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
	})
	if err != nil {
		t.Fatal(err)
	}
	address := tunnelServe(t, server)
	for _, tc := range []struct {
		name, identity, message string
		host                    ssh.PublicKey
	}{
		{"unknown-client", wrongPath, "Permission denied", hostKey.PublicKey()},
		{"unexpected-host", keyPath, "Host key verification failed", wrongKey.PublicKey()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			process := tunnelOpenSSH(t, address, tc.identity, tc.host, "-N")
			select {
			case <-process.done:
			case <-time.After(5 * time.Second):
				t.Fatal("OpenSSH did not reject the invalid key")
			}
			var exit *exec.ExitError
			if !errors.As(process.err, &exit) || exit.ExitCode() != 255 || !strings.Contains(process.output.String(), tc.message) {
				t.Fatalf("OpenSSH did not reject the intended authentication failure: %v, %s", process.err, process.output.String())
			}
		})
	}
}
