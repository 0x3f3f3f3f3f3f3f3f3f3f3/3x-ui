package policy_test

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/proxyman/command"
	corenet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/proxy"
	coressh "github.com/xtls/xray-core/proxy/ssh"
	"github.com/xtls/xray-core/testing/testauthority"
	"golang.org/x/crypto/ssh"
	netproxy "golang.org/x/net/proxy"
)

type sshMaterial struct {
	host, user         ssh.Signer
	hostFile, userFile string
}

func sshKey(t *testing.T, name string) (ssh.Signer, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "ephemeral native SSH test")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(file, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return signer, file
}

func sshConfig(t *testing.T, sshPort, tunnelPort, target int) (map[string]any, sshMaterial) {
	t.Helper()
	host, hostFile := sshKey(t, "business-host")
	user, userFile := sshKey(t, "business-user")
	m := sshMaterial{host, user, hostFile, userFile}
	c := loopbackTunnelConfig(0, "tcp", false, tunnelPort, target)
	c["inbounds"] = append(c["inbounds"].([]any), map[string]any{
		"tag": "ssh", "listen": "127.0.0.1", "port": sshPort, "protocol": "ssh",
		"settings": map[string]any{"hostKeyFile": hostFile, "users": []any{map[string]any{
			"username": "business", "publicKeys": []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(user.PublicKey())))},
			"clientId": "owner", "email": "business-account",
		}}},
	})
	c["routing"] = map[string]any{"rules": []any{map[string]any{
		"type": "field", "inboundTag": []string{"owned", "ssh"}, "outboundTag": "direct",
	}}}
	return c, m
}

func sshSettings(c map[string]any) map[string]any {
	return c["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)
}

func sshNativeClient(t *testing.T, listen int, m sshMaterial) *ssh.Client {
	t.Helper()
	c, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen), &ssh.ClientConfig{
		User: "business", Auth: []ssh.AuthMethod{ssh.PublicKeys(m.user)},
		HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey()), Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func sshChannel(t *testing.T, client *ssh.Client, target int) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", target))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func sshExchange(t *testing.T, c net.Conn, payload []byte) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := c.Write(payload)
		if err == nil {
			reply := make([]byte, len(payload))
			_, err = io.ReadFull(c, reply)
			if err == nil && !bytes.Equal(reply, payload) {
				err = fmt.Errorf("SSH payload differs: %q", reply)
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		c.Close()
		t.Fatal("SSH payload timed out")
	}
}

type sshProcess struct {
	done   chan struct{}
	err    error
	output bytes.Buffer
}

func openSSH(t *testing.T, listen int, m sshMaterial, options ...string) *sshProcess {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Fatal("standard OpenSSH client required", err)
	}
	known := filepath.Join(t.TempDir(), "known_hosts")
	line := fmt.Sprintf("[127.0.0.1]:%d %s", listen, ssh.MarshalAuthorizedKey(m.host.PublicKey()))
	if err := os.WriteFile(known, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	args := []string{"-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + known, "-o", "IdentitiesOnly=yes", "-o", "ExitOnForwardFailure=yes", "-i", m.userFile, "-p", fmt.Sprint(listen)}
	args = append(args, options...)
	args = append(args, "business@127.0.0.1")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	p := &sshProcess{done: make(chan struct{})}
	cmd.Stderr = &p.output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { cancel(); <-p.done })
	return p
}

func sshWaitFlow(t *testing.T, listen int, process *sshProcess) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-process.done:
			t.Fatalf("OpenSSH stopped: %v %s", process.err, process.output.String())
		default:
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listen), 100*time.Millisecond)
		if err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("OpenSSH forward did not listen")
	return nil
}

func sshEngine(instance *core.Instance) *clientpolicy.Engine {
	return instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
}

func TestSSHOpenSSHDirectSharesTunnelLedgerAndDisable(t *testing.T) {
	target, received := loopbackEchoTarget(t, "tcp")
	sshPort, tunnelPort := port(t), port(t)
	config, material := sshConfig(t, sshPort, tunnelPort, target)
	instance := start(t, loopbackJSON(t, config))
	localPort, dynamicPort := port(t), port(t)
	local := openSSH(t, sshPort, material, "-N", "-L", fmt.Sprintf("127.0.0.1:%d:localhost:%d", localPort, target))
	first := sshWaitFlow(t, localPort, local)
	exchange(t, first, []byte("local!"))
	dynamic := openSSH(t, sshPort, material, "-N", "-D", fmt.Sprintf("127.0.0.1:%d", dynamicPort))
	probe := sshWaitFlow(t, dynamicPort, dynamic)
	probe.Close()
	dialer, err := netproxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", dynamicPort), nil, &net.Dialer{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	second, err := dialer.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", target))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	exchange(t, second, []byte("socks!"))
	tunnel := loopbackFlow(t, "tcp", tunnelPort)
	exchange(t, tunnel, []byte("tunnel"))
	engine := sshEngine(instance)
	snapshot, err := engine.Snapshot("owner")
	want := clientpolicy.Usage{RawUpload: 18, RawDownload: 18, BilledBytes: 54}
	if err != nil || snapshot.Usage != want || received.Load() != 18 {
		t.Fatalf("SSH payload ledger: %+v err=%v target=%d; want %+v", snapshot, err, received.Load(), want)
	}
	policy, _, err := engine.GetClient("owner")
	if err != nil {
		t.Fatal(err)
	}
	policy.Version++
	policy.Enabled = false
	if err := testauthority.Apply(t, engine, policy); err != nil {
		t.Fatal(err)
	}
	for _, c := range []net.Conn{first, second, tunnel} {
		assertPasswordProxyClosed(t, c)
	}
	for _, p := range []*sshProcess{local, dynamic} {
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			t.Fatal("disabled SSH transport stayed alive")
		}
	}
}

func TestSSHChannelEOFAndCredentialRemovalPreserveTunnelSibling(t *testing.T) {
	target, received := loopbackEchoTarget(t, "tcp")
	sshPort, tunnelPort := port(t), port(t)
	config, m := sshConfig(t, sshPort, tunnelPort, target)
	instance := start(t, loopbackJSON(t, config))
	client := sshNativeClient(t, sshPort, m)
	first, second := sshChannel(t, client, target), sshChannel(t, client, target)
	sshExchange(t, first, []byte("first!"))
	first.Close()
	sshExchange(t, second, []byte("second"))
	if _, err := client.NewSession(); err == nil {
		t.Fatal("SSH session channel accepted shell/subsystem capability")
	}
	wrong, _ := sshKey(t, "wrong-user")
	if c, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), &ssh.ClientConfig{User: "business", Auth: []ssh.AuthMethod{ssh.PublicKeys(wrong)}, HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey()), Timeout: time.Second}); err == nil {
		c.Close()
		t.Fatal("unconfigured key authenticated")
	}
	tunnel := loopbackFlow(t, "tcp", tunnelPort)
	exchange(t, tunnel, []byte("sibling"))
	manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
	handler, err := manager.GetHandler(context.Background(), "ssh")
	if err != nil {
		t.Fatal(err)
	}
	if err := (&command.RemoveUserOperation{Email: "business-account"}).ApplyInbound(context.Background(), handler); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("removed credential retained SSH transport")
	}
	exchange(t, tunnel, []byte("alive!"))
	if received.Load() != 25 {
		t.Fatalf("unexpected target payload: %d", received.Load())
	}
}

func TestSSHMissingPolicyAndBlockedRouteCannotReachTarget(t *testing.T) {
	for _, mode := range []string{"missing-policy", "blocked-route"} {
		t.Run(mode, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			sshPort := port(t)
			config, m := sshConfig(t, sshPort, port(t), target)
			if mode == "missing-policy" {
				delete(config, "clientPolicy")
			} else {
				config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"ssh"}, "outboundTag": "block"}}}
			}
			start(t, loopbackJSON(t, config))
			client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), &ssh.ClientConfig{User: "business", Auth: []ssh.AuthMethod{ssh.PublicKeys(m.user)}, HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey()), Timeout: time.Second})
			if err == nil {
				defer client.Close()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				c, err := client.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", target))
				if err == nil {
					defer c.Close()
					c.Write([]byte("denied"))
					done := make(chan error, 1)
					go func() { _, e := c.Read(make([]byte, 1)); done <- e }()
					select {
					case e := <-done:
						if e == nil {
							t.Fatal("blocked SSH received target reply")
						}
					case <-time.After(time.Second):
						t.Fatal("blocked channel retained connection")
					}
				}
			}
			if received.Load() != 0 {
				t.Fatalf("denied SSH reached target: %d", received.Load())
			}
		})
	}
}

func sshReverseSettings() map[string]any {
	return map[string]any{"enabled": true, "bindAddresses": []string{"127.0.0.1"}, "portFrom": 1024, "portTo": 65535, "sourceCIDRs": []string{"127.0.0.0/8"}, "maxListeners": 1, "allowPortZero": true}
}

func TestSSHOpenSSHReverseMetersClientRelativeDirection(t *testing.T) {
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close() })
	observed := make(chan string, 1)
	go func() {
		c, err := target.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		input := make([]byte, 5)
		_, err = io.ReadFull(c, input)
		if err == nil {
			observed <- string(input)
			c.Write([]byte("up!"))
		}
	}()
	sshPort := port(t)
	config, m := sshConfig(t, sshPort, port(t), target.Addr().(*net.TCPAddr).Port)
	sshSettings(config)["reverse"] = sshReverseSettings()
	instance := start(t, loopbackJSON(t, config))
	reversePort := port(t)
	process := openSSH(t, sshPort, m, "-N", "-R", fmt.Sprintf("%d:%s", reversePort, target.Addr()))
	peer := sshWaitFlow(t, reversePort, process)
	peer.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := peer.Write([]byte("down!")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 3)
	if _, err := io.ReadFull(peer, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "up!" {
		t.Fatalf("reverse reply %q", reply)
	}
	select {
	case input := <-observed:
		if input != "down!" {
			t.Fatalf("reverse target got %q", input)
		}
	case <-time.After(time.Second):
		t.Fatal("reverse target did not receive payload")
	}
	snapshot, err := sshEngine(instance).Snapshot("owner")
	if err != nil || snapshot.Usage != (clientpolicy.Usage{RawUpload: 3, RawDownload: 5, BilledBytes: 12}) {
		t.Fatalf("reverse direction ledger: %+v %v", snapshot, err)
	}
}

func TestSSHReversePortZeroCancellationAndDisableReleaseOwnedResources(t *testing.T) {
	target, _ := loopbackEchoTarget(t, "tcp")
	sshPort := port(t)
	config, m := sshConfig(t, sshPort, port(t), target)
	sshSettings(config)["reverse"] = sshReverseSettings()
	instance := start(t, loopbackJSON(t, config))
	client := sshNativeClient(t, sshPort, m)
	listener, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if listener.Addr().(*net.TCPAddr).Port < 1024 {
		t.Fatalf("allocated port outside authorized range: %s", listener.Addr())
	}
	if extra, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
		extra.Close()
		t.Fatal("reverse listener count exceeded")
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := listener.Accept()
		if err == nil {
			accepted <- c
			io.Copy(c, c)
			c.Close()
		}
	}()
	peer, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	exchange(t, peer, []byte("reverse"))
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("forwarded channel missing")
	}
	connections, err := sshEngine(instance).Connections("owner")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range connections {
		if strings.HasPrefix(c.OriginalTarget, "ssh-reverse-listener:") {
			found = true
			if c.ActualTarget != "unknown-client-target" {
				t.Fatalf("invented reverse target: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("reverse stream absent from policy registry")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	assertPasswordProxyClosed(t, peer)
	bound, err := net.Listen("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("cancel retained reverse listener: %v", err)
	}
	bound.Close()
	second, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	engine := sshEngine(instance)
	p, _, err := engine.GetClient("owner")
	if err != nil {
		t.Fatal(err)
	}
	p.Version++
	p.Enabled = false
	if err := testauthority.Apply(t, engine, p); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle transport survived disable")
	}
	bound, err = net.Listen("tcp", second.Addr().String())
	if err != nil {
		t.Fatalf("disable retained reverse listener: %v", err)
	}
	bound.Close()
}

func TestSSHReverseAuthorizationChecksBindPortAndRealSource(t *testing.T) {
	for _, mode := range []string{"disabled", "bind", "port", "source"} {
		t.Run(mode, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			sshPort := port(t)
			config, m := sshConfig(t, sshPort, port(t), target)
			address := "127.0.0.1:0"
			if mode != "disabled" {
				reverse := sshReverseSettings()
				sshSettings(config)["reverse"] = reverse
				if mode == "bind" {
					address = "0.0.0.0:0"
				}
				if mode == "port" {
					reverse["portFrom"] = 40000
					reverse["portTo"] = 40001
					address = "127.0.0.1:39999"
				}
				if mode == "source" {
					reverse["sourceCIDRs"] = []string{"192.0.2.0/24"}
				}
			}
			instance := start(t, loopbackJSON(t, config))
			client := sshNativeClient(t, sshPort, m)
			l, err := client.Listen("tcp", address)
			if mode != "source" {
				if err == nil {
					l.Close()
					t.Fatal("unauthorized reverse listener accepted")
				}
				if !strings.Contains(err.Error(), "request denied") {
					t.Fatalf("unexpected reverse rejection: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				peer, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer peer.Close()
				peer.Write([]byte("denied"))
				assertPasswordProxyClosed(t, peer)
			}
			snapshot, err := sshEngine(instance).Snapshot("owner")
			if err != nil || snapshot.Usage != (clientpolicy.Usage{}) || received.Load() != 0 {
				t.Fatalf("denied reverse payload charged/reached target: %+v %v target=%d", snapshot, err, received.Load())
			}
		})
	}
}

func TestSSHConnectionChannelHandshakeAndIdleBounds(t *testing.T) {
	target, _ := loopbackEchoTarget(t, "tcp")
	sshPort := port(t)
	config, m := sshConfig(t, sshPort, port(t), target)
	settings := sshSettings(config)
	settings["maxChannelsPerConnection"] = 1
	settings["maxConnectionsPerUser"] = 1
	settings["handshakeTimeoutSeconds"] = 1
	settings["idleTimeoutSeconds"] = 1
	start(t, loopbackJSON(t, config))
	client := sshNativeClient(t, sshPort, m)
	first := sshChannel(t, client, target)
	sshExchange(t, first, []byte("first!"))
	if c, err := client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", target)); err == nil {
		c.Close()
		t.Fatal("channel bound exceeded")
	} else {
		var denied *ssh.OpenChannelError
		if !errors.As(err, &denied) || denied.Reason != ssh.ResourceShortage {
			t.Fatalf("channel limit rejection: %v", err)
		}
	}
	if c, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), &ssh.ClientConfig{User: "business", Auth: []ssh.AuthMethod{ssh.PublicKeys(m.user)}, HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey()), Timeout: time.Second}); err == nil {
		c.Close()
		t.Fatal("per-client SSH connection bound exceeded")
	}
	sshExchange(t, first, []byte("alive!"))
	slow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	slow.SetReadDeadline(time.Now().Add(2500 * time.Millisecond))
	_, err = io.ReadAll(slow)
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("slow SSH handshake retained transport")
	}
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("idle SSH transport retained connection")
	}
}

func sshReferenceServer(t *testing.T, m sshMaterial) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var peers []net.Conn
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, p := range peers {
			p.Close()
		}
	})
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if c.User() != "business" || !bytes.Equal(key.Marshal(), m.user.PublicKey().Marshal()) {
			return nil, fmt.Errorf("reference denied credential")
		}
		return nil, nil
	}}
	cfg.AddHostKey(m.host)
	go func() {
		for {
			raw, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			peers = append(peers, raw)
			mu.Unlock()
			go func() {
				defer raw.Close()
				raw.SetDeadline(time.Now().Add(3 * time.Second))
				server, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer server.Close()
				raw.SetDeadline(time.Time{})
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					var p struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if incoming.ChannelType() != "direct-tcpip" || ssh.Unmarshal(incoming.ExtraData(), &p) != nil {
						incoming.Reject(ssh.Prohibited, "reference permits direct TCP only")
						continue
					}
					peer, err := net.DialTimeout("tcp", net.JoinHostPort(p.Host, fmt.Sprint(p.Port)), time.Second)
					if err != nil {
						incoming.Reject(ssh.ConnectionFailed, "reference dial failed")
						continue
					}
					ch, reqs, err := incoming.Accept()
					if err != nil {
						peer.Close()
						continue
					}
					go ssh.DiscardRequests(reqs)
					go func() {
						defer peer.Close()
						defer ch.Close()
						done := make(chan struct{})
						go func() { io.Copy(ch, peer); ch.CloseWrite(); close(done) }()
						io.Copy(peer, ch)
						peer.Close()
						<-done
					}()
				}
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func TestSSHNativeOutboundStrictPinAndRoutedPayload(t *testing.T) {
	for _, correct := range []bool{true, false} {
		t.Run(fmt.Sprint(correct), func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			listen := port(t)
			config, m := sshConfig(t, port(t), listen, target)
			config["inbounds"] = config["inbounds"].([]any)[:1]
			upstream := sshReferenceServer(t, m)
			pin := m.host
			if !correct {
				pin, _ = sshKey(t, "wrong-host")
			}
			config["outbounds"] = append(config["outbounds"].([]any), map[string]any{"protocol": "ssh", "tag": "ssh-upstream", "settings": map[string]any{"address": "127.0.0.1", "port": upstream, "username": "business", "privateKeyFile": m.userFile, "hostKey": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pin.PublicKey())))}})
			config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"owned"}, "outboundTag": "ssh-upstream"}}}
			instance := start(t, loopbackJSON(t, config))
			flow := loopbackFlow(t, "tcp", listen)
			if correct {
				exchange(t, flow, []byte("sshout"))
				snapshot, err := sshEngine(instance).Snapshot("owner")
				if err != nil || snapshot.Usage != (clientpolicy.Usage{RawUpload: 6, RawDownload: 6, BilledBytes: 18}) || received.Load() != 6 {
					t.Fatalf("native SSH outbound ledger: %+v %v target=%d", snapshot, err, received.Load())
				}
			} else {
				flow.Write([]byte("denied"))
				assertPasswordProxyClosed(t, flow)
				if received.Load() != 0 {
					t.Fatal("wrong host pin reached target")
				}
			}
		})
	}
}

func TestSSHAndTunnelShareLiveDirectionalRates(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			sshPort, tunnelPort := port(t), port(t)
			config, m := sshConfig(t, sshPort, tunnelPort, target)
			instance := start(t, loopbackJSON(t, config))
			client := sshNativeClient(t, sshPort, m)
			flow := sshChannel(t, client, target)
			tunnel := loopbackFlow(t, "tcp", tunnelPort)
			sshExchange(t, flow, []byte("begin!"))
			exchange(t, tunnel, []byte("begin!"))
			engine := sshEngine(instance)
			p, _, err := engine.GetClient("owner")
			if err != nil {
				t.Fatal(err)
			}
			p.Version++
			if direction == "upload" {
				p.UploadRate = 1
			} else {
				p.DownloadRate = 1
			}
			if err := testauthority.Apply(t, engine, p); err != nil {
				t.Fatal(err)
			}
			sshExchange(t, flow, bytes.Repeat([]byte{'x'}, 65536))
			done := make(chan error, 1)
			go func() {
				_, err := tunnel.Write([]byte("queued"))
				if err == nil {
					got := make([]byte, 6)
					_, err = io.ReadFull(tunnel, got)
					if err == nil && string(got) != "queued" {
						err = fmt.Errorf("queued response %q", got)
					}
				}
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("%s bucket did not aggregate SSH and Tunnel: %v", direction, err)
			case <-time.After(200 * time.Millisecond):
			}
			if direction == "download" {
				deadline := time.Now().Add(time.Second)
				for received.Load() != 65554 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if received.Load() != 65554 {
					t.Fatalf("queued upload missing: %d", received.Load())
				}
			}
			p.Version++
			p.UploadRate = 0
			p.DownloadRate = 0
			p.Multiplier = 500000
			if err := testauthority.Apply(t, engine, p); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("rate update failed to release queued stream")
			}
			billed := uint64(196650)
			if direction == "download" {
				billed = 196656
			}
			snapshot, err := engine.Snapshot("owner")
			if err != nil || snapshot.Usage != (clientpolicy.Usage{RawUpload: 65554, RawDownload: 65554, BilledBytes: billed}) {
				t.Fatalf("live SSH rate/multiplier ledger %+v %v", snapshot, err)
			}
		})
	}
}

func TestSSHCancelledPendingReverseOpenCannotLeakAcceptedChannel(t *testing.T) {
	target, _ := loopbackEchoTarget(t, "tcp")
	sshPort := port(t)
	config, m := sshConfig(t, sshPort, port(t), target)
	sshSettings(config)["reverse"] = sshReverseSettings()
	start(t, loopbackJSON(t, config))
	raw, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(2 * time.Second))
	conn, channels, requests, err := ssh.NewClientConn(raw, raw.RemoteAddr().String(), &ssh.ClientConfig{User: "business", Auth: []ssh.AuthMethod{ssh.PublicKeys(m.user)}, HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw.SetDeadline(time.Time{})
	go ssh.DiscardRequests(requests)
	ok, response, err := conn.SendRequest("tcpip-forward", true, ssh.Marshal(struct {
		Address string
		Port    uint32
	}{"127.0.0.1", 0}))
	if err != nil || !ok {
		t.Fatalf("reverse request denied %v %v", ok, err)
	}
	var allocated struct{ Port uint32 }
	if err := ssh.Unmarshal(response, &allocated); err != nil {
		t.Fatal(err)
	}
	peer, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", allocated.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var pending ssh.NewChannel
	select {
	case pending = <-channels:
	case <-time.After(time.Second):
		t.Fatal("forwarded open missing")
	}
	ok, _, err = conn.SendRequest("cancel-tcpip-forward", true, ssh.Marshal(struct {
		Address string
		Port    uint32
	}{"127.0.0.1", allocated.Port}))
	if err != nil || !ok {
		t.Fatalf("cancel failed %v %v", ok, err)
	}
	ch, reqs, err := pending.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	done := make(chan error, 1)
	go func() { _, err := ch.Read(make([]byte, 1)); done <- err }()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("cancelled late channel read: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled reverse listener leaked late accepted channel")
	}
	direct, reqs, err := conn.OpenChannel("direct-tcpip", ssh.Marshal(struct {
		Host       string
		Port       uint32
		Origin     string
		OriginPort uint32
	}{"127.0.0.1", uint32(target), "127.0.0.1", 12345}))
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	go ssh.DiscardRequests(reqs)
	direct.Write([]byte("alive!"))
	reply := make([]byte, 6)
	if _, err := io.ReadFull(direct, reply); err != nil || string(reply) != "alive!" {
		t.Fatalf("sibling channel after reverse cancel %q %v", reply, err)
	}
}

type sshRevokingSigner struct {
	ssh.Signer
	replace func()
}

func (s sshRevokingSigner) Sign(random io.Reader, data []byte) (*ssh.Signature, error) {
	s.replace()
	return s.Signer.Sign(random, data)
}

func TestSSHVerifiedAuthenticationCannotTransferRevokedOfferToReplacement(t *testing.T) {
	target, _ := loopbackEchoTarget(t, "tcp")
	sshPort := port(t)
	config, m := sshConfig(t, sshPort, port(t), target)
	instance := start(t, loopbackJSON(t, config))
	engine := sshEngine(instance)
	if err := testauthority.Apply(t, engine, clientpolicy.Policy{ClientID: "replacement-owner", Version: 1, Enabled: true, Multiplier: 1000000, BurstBytes: 65536}); err != nil {
		t.Fatal(err)
	}
	manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
	handler, err := manager.GetHandler(context.Background(), "ssh")
	if err != nil {
		t.Fatal(err)
	}
	users := handler.(proxy.GetInbound).GetInbound().(proxy.UserManager)
	revoking := sshRevokingSigner{Signer: m.user, replace: func() {
		if err := users.RemoveUser(context.Background(), "business-account"); err != nil {
			t.Fatal(err)
		}
		if err := users.AddUser(context.Background(), &protocol.MemoryUser{ClientID: "replacement-owner", Email: "replacement-account", Account: &coressh.Account{Username: "business", PublicKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(m.user.PublicKey())))}}}); err != nil {
			t.Fatal(err)
		}
	}}
	client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), &ssh.ClientConfig{User: "business", Auth: []ssh.AuthMethod{ssh.PublicKeys(revoking)}, HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey()), Timeout: time.Second})
	if err == nil {
		client.Close()
		t.Fatal("revoked offered credential authenticated as a replacement identity")
	}
	if !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("unexpected revoked offer rejection: %v", err)
	}
	snapshot, err := engine.Snapshot("replacement-owner")
	if err != nil || snapshot.ActiveSessions != 0 || snapshot.Usage != (clientpolicy.Usage{}) {
		t.Fatalf("replacement identity inherited pending authentication: %+v %v", snapshot, err)
	}
}

func TestSSHQuotaAndExpiryCloseIdleTransportAndTunnelSibling(t *testing.T) {
	for _, reason := range []string{"quota", "expiry"} {
		t.Run(reason, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			sshPort, tunnelPort := port(t), port(t)
			config, m := sshConfig(t, sshPort, tunnelPort, target)
			instance := start(t, loopbackJSON(t, config))
			client := sshNativeClient(t, sshPort, m)
			channel := sshChannel(t, client, target)
			sshExchange(t, channel, []byte("shared"))
			tunnel := loopbackFlow(t, "tcp", tunnelPort)
			idle := sshNativeClient(t, sshPort, m)
			engine := sshEngine(instance)
			p, _, err := engine.GetClient("owner")
			if err != nil {
				t.Fatal(err)
			}
			p.Version++
			wantReason := clientpolicy.ReasonQuota
			if reason == "quota" {
				p.QuotaBytes = 18
			} else {
				p.ExpiresAt = time.Now().Add(200 * time.Millisecond).UnixMilli()
				wantReason = clientpolicy.ReasonExpired
			}
			if err := testauthority.Apply(t, engine, p); err != nil {
				t.Fatal(err)
			}
			for _, c := range []*ssh.Client{client, idle} {
				done := make(chan error, 1)
				go func(c *ssh.Client) { done <- c.Wait() }(c)
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("policy fence retained authenticated SSH transport")
				}
			}
			assertPasswordProxyClosed(t, tunnel)
			snap, err := engine.Snapshot("owner")
			if err != nil || snap.Reasons != wantReason || snap.ActiveSessions != 0 || snap.Usage != (clientpolicy.Usage{RawUpload: 6, RawDownload: 6, BilledBytes: 18}) || received.Load() != 6 {
				t.Fatalf("policy fence changed payload or retained leases: %+v err=%v target=%d", snap, err, received.Load())
			}
			denied, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), &ssh.ClientConfig{User: "business", Auth: []ssh.AuthMethod{ssh.PublicKeys(m.user)}, HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey()), Timeout: time.Second})
			if err == nil {
				denied.Close()
				t.Fatal("fenced policy authenticated a fresh SSH transport")
			}
		})
	}
}

func TestSSHRestartRetainsBusinessHostKeyAndSharedUsage(t *testing.T) {
	target, received := loopbackEchoTarget(t, "tcp")
	sshPort, tunnelPort := port(t), port(t)
	config, m := sshConfig(t, sshPort, tunnelPort, target)
	state := filepath.Join(t.TempDir(), "ssh-policy.db")
	if err := clientpolicy.CreateStore(state, "ssh-restart"); err != nil {
		t.Fatal(err)
	}
	policy := config["clientPolicy"].(map[string]any)
	policy["stateFile"], policy["instanceId"] = state, "ssh-restart"
	for epoch := uint64(1); epoch <= 2; epoch++ {
		instance := start(t, loopbackJSON(t, config))
		client := sshNativeClient(t, sshPort, m)
		channel := sshChannel(t, client, target)
		sshExchange(t, channel, []byte("sshkey"))
		channel.Close()
		client.Close()
		tunnel := loopbackFlow(t, "tcp", tunnelPort)
		exchange(t, tunnel, []byte("tunnel"))
		tunnel.Close()
		snap, err := sshEngine(instance).Snapshot("owner")
		if err != nil || snap.Epoch != epoch || snap.UncertainBytes != 0 || snap.Usage != (clientpolicy.Usage{RawUpload: 12 * epoch, RawDownload: 12 * epoch, BilledBytes: 36 * epoch}) {
			t.Fatalf("restart reset shared usage: %+v err=%v", snap, err)
		}
		if err := instance.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if received.Load() != 24 {
		t.Fatalf("independent target received %d bytes", received.Load())
	}
}

type sshBlockedControlConn struct {
	net.Conn
	paused    atomic.Bool
	blocked   chan struct{}
	closed    chan struct{}
	blockOnce sync.Once
	closeOnce sync.Once
}

func (c *sshBlockedControlConn) Write(p []byte) (int, error) {
	if c.paused.Load() {
		c.blockOnce.Do(func() { close(c.blocked) })
		<-c.closed
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(p)
}

func (c *sshBlockedControlConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestSSHStalledControlWriteClosesWithinChannelBound(t *testing.T) {
	for _, operation := range []string{"channel-accept", "global-reply", "channel-request-reply"} {
		t.Run(operation, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			sshPort := port(t)
			config, m := sshConfig(t, sshPort, port(t), target)
			sshSettings(config)["channelOpenTimeoutSeconds"] = 1
			instance := start(t, loopbackJSON(t, config))
			handler, err := instance.GetFeature(inbound.ManagerType()).(inbound.Manager).GetHandler(context.Background(), "ssh")
			if err != nil {
				t.Fatal(err)
			}
			server := handler.(interface{ GetInbound() proxy.Inbound }).GetInbound().(*coressh.Server)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			peer, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			raw, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			blocked := &sshBlockedControlConn{Conn: raw, blocked: make(chan struct{}), closed: make(chan struct{})}
			defer blocked.Close()
			ctx := session.ContextWithInbound(context.Background(), &session.Inbound{Tag: "ssh", Conn: blocked, Source: corenet.TCPDestination(corenet.LocalHostIP, 12345)})
			done := make(chan error, 1)
			go func() {
				done <- server.Process(ctx, corenet.Network_TCP, blocked, instance.GetFeature(routing.DispatcherType()).(routing.Dispatcher))
			}()
			conn, channels, requests, err := ssh.NewClientConn(peer, listener.Addr().String(), &ssh.ClientConfig{User: "business", Auth: []ssh.AuthMethod{ssh.PublicKeys(m.user)}, HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey())})
			if err != nil {
				t.Fatal(err)
			}
			client := ssh.NewClient(conn, channels, requests)
			defer client.Close()
			var requestChannel ssh.Channel
			if operation == "channel-request-reply" {
				var requestDrain <-chan *ssh.Request
				requestChannel, requestDrain, err = client.OpenChannel("direct-tcpip", ssh.Marshal(struct {
					Host       string
					Port       uint32
					OriginHost string
					OriginPort uint32
				}{"127.0.0.1", uint32(target), "127.0.0.1", 12345}))
				if err != nil {
					t.Fatal(err)
				}
				go ssh.DiscardRequests(requestDrain)
			}
			blocked.paused.Store(true)
			go func() {
				if operation == "channel-accept" {
					channel, err := client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", target))
					if err == nil {
						channel.Close()
					}
				} else if operation == "channel-request-reply" {
					requestChannel.SendRequest("unsupported-test-request", true, nil)
				} else {
					client.SendRequest("keepalive@openssh.com", true, nil)
				}
			}()
			select {
			case <-blocked.blocked:
			case <-time.After(time.Second):
				t.Fatal("control operation did not enter blocked network write")
			}
			select {
			case <-done:
			case <-time.After(1800 * time.Millisecond):
				t.Fatal("stalled SSH control write outlived configured channel bound")
			}
			if received.Load() != 0 {
				t.Fatalf("blocked control reached payload target: %d", received.Load())
			}
			sibling := sshNativeClient(t, sshPort, m)
			sshExchange(t, sshChannel(t, sibling, target), []byte("alive"))
		})
	}
}

func sshPasswordClient(t *testing.T, listen int, m sshMaterial, password string) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen), &ssh.ClientConfig{
		User: "business", Auth: []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey()), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal("password SSH authentication failed")
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func sshAssertPasswordDenied(t *testing.T, listen int, m sshMaterial, password string) {
	t.Helper()
	client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen), &ssh.ClientConfig{
		User: "business", Auth: []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey()), Timeout: time.Second,
	})
	if err == nil {
		client.Close()
		t.Fatal("SSH password authentication unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatal("password authentication failed outside authentication phase")
	}
}

func sshAssertTransportEnded(t *testing.T, client *ssh.Client) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("removed password credential retained SSH transport")
	}
}

func TestSSHPasswordDefaultOffRejectsCorrectPassword(t *testing.T) {
	target, received := loopbackEchoTarget(t, "tcp")
	sshPort := port(t)
	config, m := sshConfig(t, sshPort, port(t), target)
	sshSettings(config)["users"].([]any)[0].(map[string]any)["password"] = "ephemeral-default-off-password"
	instance := start(t, loopbackJSON(t, config))
	sshAssertPasswordDenied(t, sshPort, m, "ephemeral-default-off-password")
	snap, err := sshEngine(instance).Snapshot("owner")
	if err != nil || snap.ActiveSessions != 0 || snap.Usage != (clientpolicy.Usage{}) || received.Load() != 0 {
		t.Fatalf("default-off password attempt acquired payload or lease: %+v %v target=%d", snap, err, received.Load())
	}
	keyClient := sshNativeClient(t, sshPort, m)
	sshExchange(t, sshChannel(t, keyClient, target), []byte("key-ok"))
	snap, err = sshEngine(instance).Snapshot("owner")
	if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 6, RawDownload: 6, BilledBytes: 18}) || received.Load() != 6 {
		t.Fatalf("public key fallback accounting: %+v %v target=%d", snap, err, received.Load())
	}
}

func TestSSHPasswordOptInRotationAndRemovalPreserveCanonicalSiblings(t *testing.T) {
	target, received := loopbackEchoTarget(t, "tcp")
	sshPort, tunnelPort := port(t), port(t)
	config, m := sshConfig(t, sshPort, tunnelPort, target)
	settings := sshSettings(config)
	settings["allowPassword"] = true
	users := settings["users"].([]any)
	users[0].(map[string]any)["password"] = "ephemeral-first-password"
	settings["users"] = append(users, map[string]any{"username": "key-sibling", "publicKeys": []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(m.user.PublicKey())))}, "clientId": "owner", "email": "key-sibling-account"})
	instance := start(t, loopbackJSON(t, config))
	engine := sshEngine(instance)
	sshAssertPasswordDenied(t, sshPort, m, "ephemeral-wrong-password")
	snap, err := engine.Snapshot("owner")
	if err != nil || snap.ActiveSessions != 0 || snap.Usage != (clientpolicy.Usage{}) || received.Load() != 0 {
		t.Fatalf("wrong password acquired payload or lease: %+v %v target=%d", snap, err, received.Load())
	}
	client := sshPasswordClient(t, sshPort, m, "ephemeral-first-password")
	idle := sshPasswordClient(t, sshPort, m, "ephemeral-first-password")
	sshExchange(t, sshChannel(t, client, target), []byte("passwd"))
	sibling, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), &ssh.ClientConfig{User: "key-sibling", Auth: []ssh.AuthMethod{ssh.PublicKeys(m.user)}, HostKeyCallback: ssh.FixedHostKey(m.host.PublicKey()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sibling.Close() })
	siblingChannel := sshChannel(t, sibling, target)
	sshExchange(t, siblingChannel, []byte("keyone"))
	tunnel := loopbackFlow(t, "tcp", tunnelPort)
	exchange(t, tunnel, []byte("tunnel"))
	snap, err = engine.Snapshot("owner")
	if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 18, RawDownload: 18, BilledBytes: 54}) || received.Load() != 18 {
		t.Fatalf("password/key/Tunnel did not share canonical ledger: %+v %v target=%d", snap, err, received.Load())
	}
	handler, err := instance.GetFeature(inbound.ManagerType()).(inbound.Manager).GetHandler(context.Background(), "ssh")
	if err != nil {
		t.Fatal(err)
	}
	manager := handler.(proxy.GetInbound).GetInbound().(proxy.UserManager)
	old := manager.GetUser(context.Background(), "business-account")
	if old == nil || old.ClientID != "owner" {
		t.Fatal("password credential lost canonical client identity")
	}
	if err := (&command.RemoveUserOperation{Email: "business-account"}).ApplyInbound(context.Background(), handler); err != nil {
		t.Fatal(err)
	}
	sshAssertTransportEnded(t, client)
	sshAssertTransportEnded(t, idle)
	newUser := &protocol.User{ClientId: "owner", Email: "business-account", Account: serial.ToTypedMessage(&coressh.Account{Username: "business", Password: "ephemeral-rotated-password"})}
	if err := (&command.AddUserOperation{User: newUser}).ApplyInbound(context.Background(), handler); err != nil {
		t.Fatal(err)
	}
	fresh := manager.GetUser(context.Background(), "business-account")
	if fresh == nil || fresh == old || fresh.ClientID != "owner" {
		t.Fatal("password rotation reused revoked credential or replaced client identity")
	}
	sshAssertPasswordDenied(t, sshPort, m, "ephemeral-first-password")
	rotated := sshPasswordClient(t, sshPort, m, "ephemeral-rotated-password")
	rotatedIdle := sshPasswordClient(t, sshPort, m, "ephemeral-rotated-password")
	sshExchange(t, sshChannel(t, rotated, target), []byte("rotate"))
	sshExchange(t, siblingChannel, []byte("keytwo"))
	exchange(t, tunnel, []byte("alive!"))
	if err := (&command.RemoveUserOperation{Email: "business-account"}).ApplyInbound(context.Background(), handler); err != nil {
		t.Fatal(err)
	}
	sshAssertTransportEnded(t, rotated)
	sshAssertTransportEnded(t, rotatedIdle)
	sshAssertPasswordDenied(t, sshPort, m, "ephemeral-rotated-password")
	sshExchange(t, siblingChannel, []byte("keyend"))
	exchange(t, tunnel, []byte("finish"))
	snap, err = engine.Snapshot("owner")
	if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 48, RawDownload: 48, BilledBytes: 144}) || received.Load() != 48 {
		t.Fatalf("password lifecycle damaged sibling or ledger: %+v %v target=%d", snap, err, received.Load())
	}
}
