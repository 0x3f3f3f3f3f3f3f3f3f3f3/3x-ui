package sshtunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func tunnelConnectEventually(t *testing.T, address string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("requested remote listener did not start at %s", address)
	return nil
}

func TestOpenSSHAuthorizedReverseForwardingAccountsClientDirectionsAndCleansUp(t *testing.T) {
	db, ledger, controller, client := tunnelDB(t)
	hostKey, _ := tunnelKey(t)
	userKey, identity := tunnelKey(t)
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	request := bytes.Repeat([]byte("to-client"), 1024)
	response := bytes.Repeat([]byte("from-client"), 128)
	targetDone := make(chan error, 1)
	go func() {
		conn, err := target.Accept()
		if err != nil {
			targetDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, len(request))
		_, err = io.ReadFull(conn, got)
		if err == nil && !bytes.Equal(got, request) {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			_, err = conn.Write(response)
		}
		targetDone <- err
	}()
	reverse := tunnelLocalAddress(t)
	_, portText, _ := net.SplitHostPort(reverse)
	port, _ := strconv.Atoi(portText)
	server, err := NewServer(Config{
		InboundTag: "ssh-reverse", HostKey: hostKey,
		Clients: []Client{{PolicyID: client.PolicyID, Username: "tunnel-user", PublicKeys: []ssh.PublicKey{userKey.PublicKey()}, Reverse: []ReverseRule{{Address: "127.0.0.1", Port: uint16(port)}}}},
	}, controller, func(ctx context.Context, target Destination) (io.ReadWriteCloser, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
	})
	if err != nil {
		t.Fatal(err)
	}
	address := tunnelServe(t, server)
	process := tunnelOpenSSH(t, address, identity, hostKey.PublicKey(), "-N", "-R", reverse+":"+target.Addr().String())
	conn := tunnelConnectEventually(t, reverse)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(response))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, response) {
		t.Fatalf("actual OpenSSH -R lost its response: %v", err)
	}
	if err := <-targetDone; err != nil {
		t.Fatal(err)
	}
	account, err := ledger.Read(context.Background(), client.PolicyID)
	if err != nil || account.Up != int64(len(response)) || account.Down != int64(len(request)) || account.Billed != int64(2*(len(response)+len(request))) {
		t.Fatalf("reverse forwarding lost the SSH client's direction or billed twice: %+v, %v", account, err)
	}
	start := time.Now()
	if err := db.Model(&client).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(1250 * time.Millisecond):
		t.Fatal("disabled reverse-forward client retained its SSH session")
	}
	var reclaimed net.Listener
	for time.Since(start) < 1250*time.Millisecond {
		reclaimed, err = net.Listen("tcp", reverse)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatalf("revoked reverse listener was not released within 1.25s: %v", err)
	}
	_ = reclaimed.Close()
	t.Logf("OpenSSH -R: up=%d down=%d billed=%d; revocation/listener cleanup=%v", account.Up, account.Down, account.Billed, time.Since(start))
}

func TestSSHRemoteListenAuthorizationCancellationAndCapacity(t *testing.T) {
	_, _, controller, client := tunnelDB(t)
	hostKey, _ := tunnelKey(t)
	userKey, _ := tunnelKey(t)
	server, err := NewServer(Config{
		InboundTag: "ssh-reverse-acl", HostKey: hostKey,
		Clients: []Client{{PolicyID: client.PolicyID, Username: "tunnel-user", PublicKeys: []ssh.PublicKey{userKey.PublicKey()}, Reverse: []ReverseRule{{Address: "127.0.0.1", Port: 0}}}},
	}, controller, func(ctx context.Context, target Destination) (io.ReadWriteCloser, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
	})
	if err != nil {
		t.Fatal(err)
	}
	address := tunnelServe(t, server)
	conn, err := ssh.Dial("tcp", address, &ssh.ClientConfig{User: "tunnel-user", Auth: []ssh.AuthMethod{ssh.PublicKeys(userKey)}, HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, request := range []struct {
		Address string
		Port    uint32
	}{{"127.0.0.2", 0}, {"127.0.0.1", 12345}, {"127.0.0.1", 65536}} {
		ok, _, err := conn.SendRequest("tcpip-forward", true, ssh.Marshal(request))
		if err != nil || ok {
			t.Fatalf("unapproved remote listener accepted: %+v, %v", request, err)
		}
	}
	var listeners []net.Listener
	for range 16 {
		listener, err := conn.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		defer listener.Close()
	}
	if listener, err := conn.Listen("tcp", "127.0.0.1:0"); err == nil {
		_ = listener.Close()
		t.Fatal("SSH client allocated more than 16 remote listeners")
	}
	released := listeners[0].Addr().String()
	if err := listeners[0].Close(); err != nil {
		t.Fatal(err)
	}
	local, err := net.Listen("tcp", released)
	if err != nil {
		t.Fatalf("cancel-tcpip-forward leaked its server listener: %v", err)
	}
	_ = local.Close()
	replacement, err := conn.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cancel did not return its listener capacity: %v", err)
	}
	_ = replacement.Close()
}

func TestReverseQuotaRejectsBeforeOpeningClientTarget(t *testing.T) {
	db, _, controller, client := tunnelDB(t)
	hostKey, _ := tunnelKey(t)
	userKey, _ := tunnelKey(t)
	server, err := NewServer(Config{
		InboundTag: "ssh-reverse-quota", HostKey: hostKey,
		Clients: []Client{{PolicyID: client.PolicyID, Username: "tunnel-user", PublicKeys: []ssh.PublicKey{userKey.PublicKey()}, Reverse: []ReverseRule{{Address: "127.0.0.1", Port: 0}}}},
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
	listener, err := sshClient.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		accepted <- err
	}()
	if err := db.Model(&client).Update("total_gb", 1).Error; err != nil {
		t.Fatal(err)
	}
	external, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	select {
	case err := <-accepted:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("exhausted reverse flow reached the client's target before policy admission: %v", err)
		}
	case <-time.After(1250 * time.Millisecond):
		t.Fatal("exhausted SSH transport was not closed")
	}
}
