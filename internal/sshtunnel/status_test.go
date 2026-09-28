package sshtunnel

import (
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func waitTunnelStatus(t *testing.T, server *Server, listening bool, connections int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := server.Status()
		if status.Listening == listening && status.AuthenticatedConnections == connections {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("SSH runtime status = %+v, want listening=%v, authenticated connections=%d", server.Status(), listening, connections)
}

func TestStatusTracksAuthenticatedTransports(t *testing.T) {
	_, _, controller, record := tunnelDB(t)
	host, _ := tunnelKey(t)
	key, _ := tunnelKey(t)
	server, err := NewServer(Config{InboundTag: "ssh-status", HostKey: host, Clients: []Client{{PolicyID: record.PolicyID, Username: "status-user", PublicKeys: []ssh.PublicKey{key.PublicKey()}, Targets: []TargetRule{{Host: "*"}}}}}, controller, tcpTestDial)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Close(); <-done })
	waitTunnelStatus(t, server, true, 0)
	unsigned, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unsigned.Close()
	if err := unsigned.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	version := make([]byte, len("SSH-2.0-3x-ui-tunnel\r\n"))
	if _, err := io.ReadFull(unsigned, version); err != nil {
		t.Fatal(err)
	}
	if string(version) != "SSH-2.0-3x-ui-tunnel\r\n" {
		t.Fatalf("unexpected SSH greeting %q", version)
	}
	waitTunnelStatus(t, server, true, 0)
	dial := func() *ssh.Client {
		client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "status-user", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	first, second := dial(), dial()
	waitTunnelStatus(t, server, true, 2)
	target := tunnelEcho(t)
	for range 2 {
		channel, err := first.Dial("tcp", target)
		if err != nil {
			t.Fatal(err)
		}
		defer channel.Close()
		timer := time.AfterFunc(time.Second, func() { _ = channel.Close() })
		defer timer.Stop()
		if _, err := channel.Write([]byte("channel-status")); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 14)
		if _, err := io.ReadFull(channel, response); err != nil || string(response) != "channel-status" {
			t.Fatalf("real SSH target response = %q, error=%v", response, err)
		}
		timer.Stop()
	}
	waitTunnelStatus(t, server, true, 2)
	_ = second.Close()
	waitTunnelStatus(t, server, true, 1)
	if err := server.UpdateClients(nil); err != nil {
		t.Fatal(err)
	}
	waitTunnelStatus(t, server, true, 0)
	server.Close()
	waitTunnelStatus(t, server, false, 0)
}

func TestStatusStopsAfterListenerFailure(t *testing.T) {
	_, _, controller, _ := tunnelDB(t)
	host, _ := tunnelKey(t)
	server, err := NewServer(Config{InboundTag: "ssh-status-failure", HostKey: host}, controller, tcpTestDial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	waitTunnelStatus(t, server, true, 0)
	_ = listener.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unexpected listener failure must be reported")
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after listener failure")
	}
	waitTunnelStatus(t, server, false, 0)
}
