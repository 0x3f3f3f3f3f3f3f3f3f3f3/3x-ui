package sshoutbound

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) (string, string) {
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

type wirePeer struct {
	config  Config
	opened  chan string
	closed  chan struct{}
	accepts atomic.Int64
}

func newWirePeer(t *testing.T, additionalHostKeys ...ssh.Signer) *wirePeer {
	t.Helper()
	hostPrivate, hostPublic := testKey(t)
	clientPrivate, clientPublic := testKey(t)
	host, _ := ssh.ParsePrivateKey([]byte(hostPrivate))
	client, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(clientPublic))
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "wire-user" || !bytes.Equal(client.Marshal(), key.Marshal()) {
			return nil, errors.New("wrong test identity")
		}
		return &ssh.Permissions{}, nil
	}}
	serverConfig.AddHostKey(host)
	for _, key := range additionalHostKeys {
		serverConfig.AddHostKey(key)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &wirePeer{
		config: Config{Address: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "wire-user", PrivateKey: clientPrivate, HostKey: hostPublic},
		opened: make(chan string, 2*MaxConnections), closed: make(chan struct{}, 2*MaxConnections),
	}
	ctx, cancel := context.WithCancel(context.Background())
	var connections sync.Map
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		connections.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
		workers.Wait()
	})
	workers.Go(func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			p.accepts.Add(1)
			connections.Store(raw, true)
			workers.Go(func() {
				defer raw.Close()
				defer connections.Delete(raw)
				if ctx.Err() != nil {
					return
				}
				_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
				conn, channels, requests, err := ssh.NewServerConn(raw, serverConfig)
				if err != nil {
					return
				}
				defer conn.Close()
				defer func() { p.closed <- struct{}{} }()
				_ = raw.SetDeadline(time.Time{})
				workers.Go(func() { ssh.DiscardRequests(requests) })
				for incoming := range channels {
					var destination struct {
						Host       string
						Port       uint32
						OriginHost string
						OriginPort uint32
					}
					if incoming.ChannelType() != "direct-tcpip" || ssh.Unmarshal(incoming.ExtraData(), &destination) != nil {
						_ = incoming.Reject(ssh.Prohibited, "only direct-tcpip")
						continue
					}
					p.opened <- net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port)))
					if destination.Host == "stall.invalid" {
						continue
					}
					stream, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					workers.Go(func() { ssh.DiscardRequests(requests) })
					if destination.Host == "blocked.invalid" {
						continue
					}
					if destination.Host == "paused.invalid" {
						workers.Go(func() {
							if _, err := io.CopyN(io.Discard, stream, 32768); err == nil {
								_, _ = stream.Write([]byte("paused"))
							}
						})
						continue
					}
					workers.Go(func() {
						defer stream.Close()
						_, _ = io.Copy(stream, stream)
						_ = stream.CloseWrite()
					})
				}
			})
		}
	})
	return p
}

func wireConnector(t *testing.T, p *wirePeer) *Connector {
	t.Helper()
	c, err := NewConnector(p.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestConnectorPreservesRemoteTargetAndEstablishedOwnership(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	for _, target := range []string{"MiXeD.Example.invalid.:443", "192.0.2.9:8443", "[2001:db8::9]:443"} {
		ctx, cancel := context.WithCancel(context.Background())
		conn, err := c.DialContext(ctx, "tcp", target)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if got := <-p.opened; got != target {
			t.Fatalf("upstream received %q, want unchanged destination %q", got, target)
		}
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte("still-open")); err != nil {
			t.Fatalf("dial context cancellation closed an established stream: %v", err)
		}
		var got [10]byte
		if _, err := io.ReadFull(conn, got[:]); err != nil || string(got[:]) != "still-open" {
			t.Fatalf("established stream payload=%q err=%v", got, err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConnectorCancelsStalledChannelAndKeepsOtherStream(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	active, err := c.DialContext(context.Background(), "tcp", "active.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	<-p.opened
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := c.DialContext(ctx, "tcp", "stall.invalid:80")
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	select {
	case got := <-p.opened:
		if got != "stall.invalid:80" {
			t.Fatal(got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive stalled channel open")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stalled open cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled SSH channel did not cancel promptly")
	}
	select {
	case <-p.closed:
	case <-time.After(time.Second):
		t.Fatal("canceled channel left its SSH transport alive")
	}
	_ = active.SetDeadline(time.Now().Add(time.Second))
	if _, err := active.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	var got [2]byte
	if _, err := io.ReadFull(active, got[:]); err != nil || string(got[:]) != "ok" {
		t.Fatalf("canceling another stream broke the active one: %q %v", got, err)
	}
}

func TestConnectorCapacityReleasesExactlyOnceAndCloseRevokes(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	connections := make([]net.Conn, 0, MaxConnections+1)
	t.Cleanup(func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
	open := func() {
		t.Helper()
		conn, err := c.DialContext(context.Background(), "tcp", "capacity.invalid:80")
		if err != nil {
			t.Fatalf("capacity released too early/late: %v", err)
		}
		connections = append(connections, conn)
	}
	full := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := c.DialContext(ctx, "tcp", "capacity.invalid:80")
		if conn != nil {
			_ = conn.Close()
		}
		if !errors.Is(err, ErrCapacity) {
			t.Fatalf("connection beyond capacity result=%v", err)
		}
	}
	for range MaxConnections {
		open()
	}
	full()
	for range 2 {
		if err := connections[0].Close(); err != nil {
			t.Fatal(err)
		}
	}
	open()
	full()
	for range 2 {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if conn, err := c.DialContext(context.Background(), "tcp", "capacity.invalid:80"); conn != nil || !errors.Is(err, ErrClosed) {
		t.Fatalf("closed connector admission: conn=%t err=%v", conn != nil, err)
	}
	deadline := time.After(2 * time.Second)
	for range MaxConnections + 1 {
		select {
		case <-p.closed:
		case <-deadline:
			t.Fatal("connector close left upstream transports alive")
		}
	}
}

func TestConnectorRejectsNonTCPAndMalformedTargetsBeforeDial(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	for _, test := range []struct {
		network, address string
		want             error
	}{
		{"udp", "127.0.0.1:53", ErrNetwork},
		{"unix", "/tmp/socket", ErrNetwork},
		{"tcp", "host.invalid", ErrTarget},
		{"tcp", "host.invalid:0", ErrTarget},
		{"tcp", "host.invalid:65536", ErrTarget},
		{"tcp", "host.invalid:http", ErrTarget},
		{"tcp", ":80", ErrTarget},
		{"tcp", "http://host.invalid:80", ErrTarget},
	} {
		conn, err := c.DialContext(context.Background(), test.network, test.address)
		if conn != nil || !errors.Is(err, test.want) {
			t.Fatalf("network=%s target=%q: conn=%t error=%v want=%v", test.network, test.address, conn != nil, err, test.want)
		}
	}
	if p.accepts.Load() != 0 {
		t.Fatalf("invalid request opened %d upstream transports", p.accepts.Load())
	}
}

func TestConnectorConfigurationRejectsMissingPinsAndSecretLeaks(t *testing.T) {
	private, public := testKey(t)
	valid := Config{Address: "localhost", Port: 22, User: "user", PrivateKey: private, HostKey: public}
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"no-pin", func(c *Config) { c.HostKey = "" }},
		{"multiple-pins", func(c *Config) { c.HostKey += public }},
		{"pin-options", func(c *Config) { c.HostKey = "no-pty " + public }},
		{"private-pin", func(c *Config) { c.HostKey = private }},
		{"no-private", func(c *Config) { c.PrivateKey = "" }},
		{"secret-input", func(c *Config) { c.PrivateKey = "do-not-disclose-this-private-key" }},
		{"wrong-passphrase", func(c *Config) { c.PrivateKeyPassphrase = "do-not-disclose-passphrase" }},
		{"bad-address", func(c *Config) { c.Address = "ssh://localhost" }},
		{"zero-port", func(c *Config) { c.Port = 0 }},
		{"oversize-port", func(c *Config) { c.Port = 65536 }},
		{"empty-user", func(c *Config) { c.User = "" }},
		{"control-user", func(c *Config) { c.User = "user\nother" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.change(&config)
			conn, err := NewConnector(config)
			if conn != nil {
				_ = conn.Close()
			}
			if !errors.Is(err, ErrConfig) || conn != nil {
				t.Fatalf("invalid configuration accepted: connector=%t err=%v", conn != nil, err)
			}
			for _, secret := range []string{private, "do-not-disclose-this-private-key", "do-not-disclose-passphrase"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("configuration error exposed secret input")
				}
			}
		})
	}
}

func TestConnectorCloseInterruptsPendingChannel(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	done := make(chan error, 1)
	go func() {
		conn, err := c.DialContext(context.Background(), "tcp", "stall.invalid:80")
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	select {
	case <-p.opened:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never received the pending request")
	}
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("connector shutdown waited for a stalled channel")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("pending open after shutdown returned %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending dial survived connector shutdown")
	}
	select {
	case <-p.closed:
	case <-time.After(time.Second):
		t.Fatal("upstream transport survived connector shutdown")
	}
}

func TestConnectorHandshakeDeadlineClosesTransport(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	private, public := testKey(t)
	c, err := NewConnector(Config{Address: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "deadline", PrivateKey: private, HostKey: public})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	peerClosed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			peerClosed <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = io.Copy(io.Discard, conn)
		peerClosed <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	conn, err := c.DialContext(ctx, "tcp", "destination.invalid:80")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("an upstream without an SSH banner established a connection")
	}
	var timeout net.Error
	if !errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &timeout) || !timeout.Timeout()) {
		t.Fatalf("stalled handshake returned %v, want deadline failure", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("caller deadline did not bound handshake: %v", elapsed)
	}
	select {
	case err := <-peerClosed:
		if err != nil {
			t.Fatalf("timed-out handshake did not close its raw transport: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out handshake transport remained open")
	}
}

func TestConnectorEncryptedPrivateKeyAuthenticates(t *testing.T) {
	p := newWirePeer(t)
	private, err := ssh.ParseRawPrivateKey([]byte(p.config.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	const passphrase = "independent-test-passphrase"
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "isolated encrypted key", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	p.config.PrivateKey = string(pem.EncodeToMemory(block))
	p.config.PrivateKeyPassphrase = passphrase
	c := wireConnector(t, p)
	conn, err := c.DialContext(context.Background(), "tcp", "encrypted.invalid:80")
	if err != nil {
		t.Fatalf("encrypted private key failed signed authentication: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("signed")); err != nil {
		t.Fatal(err)
	}
	var got [6]byte
	if _, err := io.ReadFull(conn, got[:]); err != nil || string(got[:]) != "signed" {
		t.Fatalf("encrypted-key stream payload=%q err=%v", got, err)
	}
	p.config.PrivateKeyPassphrase = "incorrect-test-passphrase"
	invalid, err := NewConnector(p.config)
	if invalid != nil {
		_ = invalid.Close()
	}
	if invalid != nil || !errors.Is(err, ErrConfig) {
		t.Fatalf("wrong passphrase accepted: connector=%t err=%v", invalid != nil, err)
	}
	if strings.Contains(err.Error(), p.config.PrivateKeyPassphrase) || strings.Contains(err.Error(), p.config.PrivateKey) {
		t.Fatal("wrong-passphrase error exposed private material")
	}
}

func TestConnectorWriteDeadlineInterruptsExhaustedSSHWindow(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	conn, err := c.DialContext(context.Background(), "tcp", "blocked.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := conn.Write(make([]byte, 4<<20))
		done <- err
	}()
	select {
	case err := <-done:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("blocked channel write returned %v, want deadline failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write deadline left a writer blocked on SSH window capacity")
	}
}

func TestConnectorReadDeadlineReturnsTimeout(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	conn, err := c.DialContext(context.Background(), "tcp", "blocked.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := conn.Read(b[:])
		done <- err
	}()
	select {
	case err := <-done:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("channel read returned %v, want deadline failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read deadline left a reader blocked")
	}
}

func TestConnectorClearedIdleDeadlinePreservesStream(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	conn, err := c.DialContext(context.Background(), "tcp", "idle.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	var got [2]byte
	if _, err := conn.Read(got[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expired read deadline returned %v", err)
	}
	if _, err := conn.Write([]byte("no")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expired write deadline returned %v", err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, got[:]); err != nil || string(got[:]) != "ok" {
		t.Fatalf("resetting an idle expired deadline broke the stream: %q %v", got, err)
	}
}

func TestConnectorDeadlineUpdateInterruptsActiveWrite(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	conn, err := c.DialContext(context.Background(), "tcp", "paused.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() {
		_, err := conn.Write(make([]byte, 4<<20))
		done <- err
	}()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var ack [6]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil || string(ack[:]) != "paused" {
		t.Fatalf("upstream did not observe the active write: %q %v", ack, err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("active write after deadline update returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("updating the deadline failed to interrupt an active write")
	}
}

func TestConnectorConcurrentWritersPreserveBytes(t *testing.T) {
	p := newWirePeer(t)
	c := wireConnector(t, p)
	conn, err := c.DialContext(context.Background(), "tcp", "concurrent.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	var writers sync.WaitGroup
	t.Cleanup(writers.Wait)
	for i := range 8 {
		writers.Go(func() {
			payload := bytes.Repeat([]byte{byte(i)}, 1024)
			for range 128 {
				if n, err := conn.Write(payload); err != nil || n != len(payload) {
					t.Errorf("writer %d wrote %d bytes: %v", i, n, err)
					return
				}
			}
		})
	}
	go func() {
		writers.Wait()
		_ = conn.(interface{ CloseWrite() error }).CloseWrite()
	}()
	response, err := io.ReadAll(conn)
	if err != nil || len(response) != 1048576 {
		t.Fatalf("concurrent writers returned %d bytes, want 1048576: %v", len(response), err)
	}
	for i := range 8 {
		if count := bytes.Count(response, []byte{byte(i)}); count != 131072 {
			t.Fatalf("writer %d bytes=%d, want 131072", i, count)
		}
	}
}

func TestConnectorNegotiatesPinnedHostKeyAmongMultipleKeys(t *testing.T) {
	ecdsaPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaHost, err := ssh.NewSignerFromKey(ecdsaPrivate)
	if err != nil {
		t.Fatal(err)
	}
	rsaPrivate, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaHost, err := ssh.NewSignerFromKey(rsaPrivate)
	if err != nil {
		t.Fatal(err)
	}
	sha2Host, err := ssh.NewSignerWithAlgorithms(rsaHost.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSASHA256})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		pin  ssh.PublicKey
	}{
		{"ed25519-among-preferred-algorithms", nil},
		{"rsa-with-sha2-signature", sha2Host.PublicKey()},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := newWirePeer(t, ecdsaHost, sha2Host)
			if test.pin != nil {
				p.config.HostKey = string(ssh.MarshalAuthorizedKey(test.pin))
			}
			c := wireConnector(t, p)
			conn, err := c.DialContext(context.Background(), "tcp", "host-selection.invalid:80")
			if err != nil {
				t.Fatalf("available pinned host key was not negotiated: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			if _, err := conn.Write([]byte("pinned")); err != nil {
				t.Fatal(err)
			}
			var got [6]byte
			if _, err := io.ReadFull(conn, got[:]); err != nil || string(got[:]) != "pinned" {
				t.Fatalf("multi-key upstream payload=%q err=%v", got, err)
			}
		})
	}
}
