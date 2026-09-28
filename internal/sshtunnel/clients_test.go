package sshtunnel

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestSSHKeyRotationRevokesOnlyRemovedCredential(t *testing.T) {
	db, _, controller, a := tunnelDB(t)
	b := model.ClientRecord{Email: uuid.NewString(), Enable: true}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: b.Email, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := controller.Configure(context.Background(), b.PolicyID, policyflow.Rates{}); err != nil {
		t.Fatal(err)
	}
	host, _ := tunnelKey(t)
	old, _ := tunnelKey(t)
	replacement, _ := tunnelKey(t)
	other, _ := tunnelKey(t)
	target := tunnelEcho(t)
	_, portText, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	clients := []Client{
		{PolicyID: a.PolicyID, Username: "client-a", PublicKeys: []ssh.PublicKey{old.PublicKey()}, Targets: []TargetRule{{Host: "127.0.0.1", Port: uint16(port)}}},
		{PolicyID: b.PolicyID, Username: "client-b", PublicKeys: []ssh.PublicKey{other.PublicKey()}, Targets: []TargetRule{{Host: "127.0.0.1", Port: uint16(port)}}},
	}
	server, err := NewServer(Config{InboundTag: "ssh-rotation", HostKey: host, Clients: clients}, controller, func(ctx context.Context, target Destination) (io.ReadWriteCloser, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
	})
	if err != nil {
		t.Fatal(err)
	}
	address := tunnelServe(t, server)
	connect := func(user string, key ssh.Signer) (*ssh.Client, net.Conn) {
		t.Helper()
		client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		conn, err := client.Dial("tcp", target)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return client, conn
	}
	echo := func(conn net.Conn) {
		t.Helper()
		if _, err := conn.Write([]byte("ok")); err != nil {
			t.Fatal(err)
		}
		var reply [2]byte
		if _, err := io.ReadFull(conn, reply[:]); err != nil || string(reply[:]) != "ok" {
			t.Fatalf("credential change interrupted an authorized channel: %q, %v", reply, err)
		}
	}
	ca, pa := connect("client-a", old)
	_, pb := connect("client-b", other)
	echo(pa)
	echo(pb)
	clients[0].PublicKeys = []ssh.PublicKey{old.PublicKey(), replacement.PublicKey()}
	if err := server.UpdateClients(clients); err != nil {
		t.Fatal(err)
	}
	echo(pa)
	_, pn := connect("client-a", replacement)
	echo(pn)
	clients[0].PublicKeys = []ssh.PublicKey{replacement.PublicKey()}
	if err := server.UpdateClients(clients); err != nil {
		t.Fatal(err)
	}
	revoked := make(chan error, 1)
	go func() { revoked <- ca.Wait() }()
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("removed SSH key kept an authenticated transport")
	}
	echo(pn)
	echo(pb)
	denied, err := ssh.Dial("tcp", address, &ssh.ClientConfig{User: "client-a", Auth: []ssh.AuthMethod{ssh.PublicKeys(old)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: 2 * time.Second})
	if denied != nil {
		_ = denied.Close()
	}
	if err == nil {
		t.Fatal("removed SSH key authenticated again")
	}
}

type pausedSigner struct {
	ssh.Signer
	started chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (s *pausedSigner) Sign(random io.Reader, data []byte) (*ssh.Signature, error) {
	s.once.Do(func() { close(s.started) })
	<-s.proceed
	return s.Signer.Sign(random, data)
}

func TestSSHRemovedKeyCannotFinishAnEarlierAuthentication(t *testing.T) {
	_, _, controller, client := tunnelDB(t)
	host, _ := tunnelKey(t)
	old, _ := tunnelKey(t)
	newKey, _ := tunnelKey(t)
	target := tunnelEcho(t)
	var dialed atomic.Int32
	definition := Client{PolicyID: client.PolicyID, Username: "client-a", PublicKeys: []ssh.PublicKey{old.PublicKey()}, Targets: []TargetRule{{Host: "*", Port: 0}}}
	server, err := NewServer(Config{InboundTag: "ssh-auth-race", HostKey: host, Clients: []Client{definition}}, controller, func(ctx context.Context, destination Destination) (io.ReadWriteCloser, error) {
		dialed.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port))))
	})
	if err != nil {
		t.Fatal(err)
	}
	address := tunnelServe(t, server)
	key := &pausedSigner{Signer: old, started: make(chan struct{}), proceed: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(key.proceed) })
	done := make(chan error, 1)
	go func() {
		conn, err := ssh.Dial("tcp", address, &ssh.ClientConfig{User: "client-a", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: 2 * time.Second})
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		stream, err := conn.Dial("tcp", target)
		if stream != nil {
			_, err = stream.Write([]byte("x"))
			var reply [1]byte
			if err == nil {
				_, err = io.ReadFull(stream, reply[:])
			}
			_ = stream.Close()
		}
		done <- err
	}()
	select {
	case <-key.started:
	case <-time.After(2 * time.Second):
		t.Fatal("SSH client did not reach signed authentication")
	}
	definition.PublicKeys = []ssh.PublicKey{newKey.PublicKey()}
	if err := server.UpdateClients([]Client{definition}); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(key.proceed) })
	select {
	case err := <-done:
		if err == nil || dialed.Load() != 0 {
			t.Fatalf("revoked key finished a cached authentication and reached the target: dials=%d, err=%v", dialed.Load(), err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked in-flight authentication remained active")
	}
}
