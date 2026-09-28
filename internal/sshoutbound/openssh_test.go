//go:build linux

package sshoutbound

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/testutil/sshdtest"
)

func testOpenSSH(t *testing.T) Config {
	t.Helper()
	endpoint := sshdtest.Start(t)
	return Config{Address: endpoint.Address, Port: endpoint.Port, User: endpoint.User, PrivateKey: endpoint.PrivateKey, HostKey: endpoint.HostKey}
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

func TestBridgeOpenSSHForwardingAndPinFailure(t *testing.T) {
	config := testOpenSSH(t)
	m := bridgeManager(t)
	outbound := Outbound{Tag: "actual-openssh", Settings: config}
	stage, err := m.Prepare([]Outbound{outbound})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var calls atomic.Int64
	received := make(chan string, 2)
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				data, _ := io.ReadAll(conn)
				received <- string(data)
				_, _ = conn.Write([]byte("response-through-ssh"))
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(target.Addr().String())
	client, err := dialBridge(t, renderedBridge(t, m, outbound), net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Write([]byte("request-through-ssh")); err != nil {
		t.Fatal(err)
	}
	if err := client.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(client)
	if err != nil || string(response) != "response-through-ssh" {
		t.Fatalf("actual bridge response=%q err=%v", response, err)
	}
	if got := <-received; got != "request-through-ssh" {
		t.Fatalf("independent SSH target received %q", got)
	}
	_, outbound.Settings.HostKey = testKey(t)
	stage, err = m.Prepare([]Outbound{outbound})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	rejected, err := dialBridge(t, renderedBridge(t, m, outbound), target.Addr().String())
	if rejected != nil {
		_ = rejected.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "connection refused") || rejected != nil {
		t.Fatalf("wrong host pin did not refuse CONNECT: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("failed pin used a direct fallback: target connections=%d", calls.Load())
	}
}
