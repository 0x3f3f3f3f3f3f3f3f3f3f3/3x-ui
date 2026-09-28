package sshoutbound

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func bridgeManager(t *testing.T) *Manager {
	t.Helper()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	_ = reservation.Close()
	m, err := NewManager(port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

type bridgeSettings struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	Pass    string `json:"pass"`
}

func renderedBridge(t *testing.T, m *Manager, outbound Outbound) bridgeSettings {
	t.Helper()
	raw, err := m.Render(outbound)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Settings bridgeSettings `json:"settings"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value.Settings
}

func dialBridge(t *testing.T, settings bridgeSettings, target string) (net.Conn, error) {
	t.Helper()
	d, err := proxy.SOCKS5("tcp", net.JoinHostPort(settings.Address, strconv.Itoa(settings.Port)), &proxy.Auth{User: settings.User, Password: settings.Pass}, &net.Dialer{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(settings.Address, strconv.Itoa(settings.Port)))
	if err != nil {
		return nil, err
	}
	_, err = d.(interface {
		DialWithConn(context.Context, net.Conn, string, string) (net.Addr, error)
	}).DialWithConn(ctx, raw, "tcp", target)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return raw, nil
}

func bridgeEcho(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != payload {
		t.Fatalf("bridge payload=%q want=%q err=%v", got, payload, err)
	}
}

func TestBridgeRequiresAuthenticationBeforeUpstreamDial(t *testing.T) {
	p := newWirePeer(t)
	m := bridgeManager(t)
	outbound := Outbound{Tag: "pinned-exit", Settings: p.config}
	endpoint := renderedBridge(t, m, outbound)
	address := net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.Port))
	probe, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("preview opened a listener: %v", err)
	}
	_ = probe.Close()
	stage, err := m.Prepare([]Outbound{outbound})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Rollback()
	stage.Commit()
	raw, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(time.Second))
	if _, err := raw.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var selection [2]byte
	if _, err := io.ReadFull(raw, selection[:]); err != nil || selection != [2]byte{5, 255} {
		t.Fatalf("no-auth selection=%v err=%v", selection, err)
	}
	wrong := endpoint
	wrong.Pass = "incorrect-test-password"
	conn, err := dialBridge(t, wrong, "private.invalid:443")
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "authentication failed") || conn != nil {
		t.Fatalf("wrong bridge password accepted: conn=%t err=%v", conn != nil, err)
	}
	if p.accepts.Load() != 0 {
		t.Fatalf("unauthorized request reached upstream %d times", p.accepts.Load())
	}
	conn, err = dialBridge(t, endpoint, "MiXeD.private.invalid.:443")
	if err != nil {
		t.Fatalf("authenticated SSH bridge did not connect: %v", err)
	}
	defer conn.Close()
	bridgeEcho(t, conn, "authenticated")
	if target := <-p.opened; target != "MiXeD.private.invalid.:443" {
		t.Fatalf("bridge changed target: %q", target)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if remaining, err := io.ReadAll(conn); err != nil || len(remaining) != 0 {
		t.Fatalf("half-close did not drain: bytes=%d err=%v", len(remaining), err)
	}
}

func TestBridgeRollbackKeepsWorkingGeneration(t *testing.T) {
	p := newWirePeer(t)
	m := bridgeManager(t)
	original := Outbound{Tag: "exit", Settings: p.config}
	stage, err := m.Prepare([]Outbound{original})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	old := renderedBridge(t, m, original)
	active, err := dialBridge(t, old, "old.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	changed := original
	_, changed.Settings.HostKey = testKey(t)
	candidate := renderedBridge(t, m, changed)
	stage, err = m.Prepare([]Outbound{changed})
	if err != nil {
		t.Fatal(err)
	}
	stage.Rollback()
	stage.Rollback()
	bridgeEcho(t, active, "old-generation-survives")
	rejected, err := dialBridge(t, candidate, "candidate.invalid:80")
	if rejected != nil {
		_ = rejected.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "authentication failed") || rejected != nil {
		t.Fatal("rolled-back generation still authenticates")
	}
	restored, err := dialBridge(t, old, "restored.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	bridgeEcho(t, restored, "still-authenticated")
	if p.accepts.Load() != 2 {
		t.Fatalf("rollback leaked a dial: got %d upstream connections", p.accepts.Load())
	}
}

func TestBridgeCommitRevokesChangedAndDeletedGenerations(t *testing.T) {
	p := newWirePeer(t)
	m := bridgeManager(t)
	a := Outbound{Tag: "changed", Settings: p.config}
	b := Outbound{Tag: "unchanged", Settings: p.config}
	stage, err := m.Prepare([]Outbound{a, b})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	aEndpoint, bEndpoint := renderedBridge(t, m, a), renderedBridge(t, m, b)
	old, err := dialBridge(t, aEndpoint, "old.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	stable, err := dialBridge(t, bEndpoint, "stable.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer stable.Close()
	_, a.Settings.HostKey = testKey(t)
	stage, err = m.Prepare([]Outbound{a, b})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Rollback()
	bridgeEcho(t, old, "alive-before-commit")
	stage.Commit()
	stage.Commit()
	assertBridgeClosed(t, old)
	bridgeEcho(t, stable, "unrelated-stream-survives")
	rejected, err := dialBridge(t, aEndpoint, "revoked.invalid:80")
	if rejected != nil {
		_ = rejected.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("old credentials were not revoked: %v", err)
	}
	stage, err = m.Prepare([]Outbound{a})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	assertBridgeClosed(t, stable)
}

func assertBridgeClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var data [1]byte
	n, err := conn.Read(data[:])
	var timeout net.Error
	if n != 0 || err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("revoked bridge remained usable: bytes=%d err=%v", n, err)
	}
}

func TestBridgeRejectedPrepareAndPortConflictPreserveState(t *testing.T) {
	p := newWirePeer(t)
	m := bridgeManager(t)
	a := Outbound{Tag: "working", Settings: p.config}
	endpoint := renderedBridge(t, m, a)
	address := net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.Port))
	blocker, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := m.Prepare([]Outbound{a})
	if stage != nil {
		stage.Rollback()
	}
	var bind *net.OpError
	if stage != nil || !errors.As(err, &bind) || bind.Op != "listen" {
		t.Fatalf("occupied bridge was not rejected: %v", err)
	}
	_ = blocker.Close()
	stage, err = m.Prepare([]Outbound{a})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	active, err := dialBridge(t, endpoint, "working.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	invalid := a
	invalid.Tag = "invalid"
	invalid.Settings.HostKey = ""
	stage, err = m.Prepare([]Outbound{a, invalid})
	if stage != nil {
		stage.Rollback()
	}
	if stage != nil || !errors.Is(err, ErrConfig) {
		t.Fatalf("invalid staged key accepted: %v", err)
	}
	bridgeEcho(t, active, "invalid-config-did-not-revoke")
	stage, err = m.Prepare([]Outbound{a})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Rollback()
	concurrent, err := m.Prepare(nil)
	if concurrent != nil {
		concurrent.Rollback()
	}
	if concurrent != nil || !errors.Is(err, ErrStaging) {
		t.Fatalf("overlapping prepare: %v", err)
	}
	stage.Commit()
	bridgeEcho(t, active, "unchanged-commit-did-not-revoke")
}

func TestBridgeStopAndRestartReleasesPortAndStreams(t *testing.T) {
	p := newWirePeer(t)
	m := bridgeManager(t)
	a := Outbound{Tag: "restart", Settings: p.config}
	endpoint := renderedBridge(t, m, a)
	stage, err := m.Prepare([]Outbound{a})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	active, err := dialBridge(t, endpoint, "restart.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	assertBridgeClosed(t, active)
	address := net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.Port))
	probe, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("stop retained bridge port: %v", err)
	}
	_ = probe.Close()
	stage, err = m.Prepare([]Outbound{a})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	restarted, err := dialBridge(t, endpoint, "restart.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	bridgeEcho(t, restarted, "restarted")
	stage, err = m.Prepare(nil)
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	assertBridgeClosed(t, restarted)
	probe, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("empty desired retained bridge port: %v", err)
	}
	_ = probe.Close()
}

func bridgeAuth(t *testing.T, endpoint bridgeSettings) net.Conn {
	t.Helper()
	client, err := net.DialTimeout("tcp", net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.Port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_, err = client.Write([]byte{5, 1, 2})
	if err != nil {
		t.Fatal(err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(client, reply[:]); err != nil || reply != [2]byte{5, 2} {
		t.Fatalf("SOCKS method=%v err=%v", reply, err)
	}
	auth := append([]byte{1, byte(len(endpoint.User))}, []byte(endpoint.User)...)
	auth = append(auth, byte(len(endpoint.Pass)))
	auth = append(auth, []byte(endpoint.Pass)...)
	if _, err := client.Write(auth); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(client, reply[:]); err != nil || reply != [2]byte{1, 0} {
		t.Fatalf("SOCKS auth=%v err=%v", reply, err)
	}
	return client
}

func TestBridgeRejectsUnsupportedRequestsBeforeSSH(t *testing.T) {
	p := newWirePeer(t)
	m := bridgeManager(t)
	outbound := Outbound{Tag: "strict", Settings: p.config}
	stage, err := m.Prepare([]Outbound{outbound})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	endpoint := renderedBridge(t, m, outbound)
	for _, test := range []struct {
		name   string
		packet []byte
		code   byte
	}{
		{"udp", []byte{5, 3, 0, 1, 127, 0, 0, 1, 0, 53}, 7},
		{"bind", []byte{5, 2, 0, 1, 127, 0, 0, 1, 0, 80}, 7},
		{"version", []byte{4, 1, 0, 1, 127, 0, 0, 1, 0, 80}, 1},
		{"reserved", []byte{5, 1, 1, 1, 127, 0, 0, 1, 0, 80}, 1},
		{"zero-port", []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 0}, 8},
		{"address-type", []byte{5, 1, 0, 7}, 8},
		{"empty-domain", []byte{5, 1, 0, 3, 0, 0, 80}, 8},
		{"invalid-domain", []byte{5, 1, 0, 3, 1, '/', 0, 80}, 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := bridgeAuth(t, endpoint)
			if _, err := client.Write(test.packet); err != nil {
				t.Fatal(err)
			}
			var response [10]byte
			if _, err := io.ReadFull(client, response[:]); err != nil || response[0] != 5 || response[1] != test.code {
				t.Fatalf("SOCKS reply=%v want code=%d err=%v", response, test.code, err)
			}
			assertBridgeClosed(t, client)
		})
	}
	if p.accepts.Load() != 0 {
		t.Fatalf("invalid requests dialed upstream %d times", p.accepts.Load())
	}
}

func TestBridgeBoundsPendingAuthentication(t *testing.T) {
	p := newWirePeer(t)
	m := bridgeManager(t)
	outbound := Outbound{Tag: "capacity", Settings: p.config}
	stage, err := m.Prepare([]Outbound{outbound})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	endpoint := renderedBridge(t, m, outbound)
	address := net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.Port))
	clients := make([]net.Conn, 0, MaxBridgeConnections)
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.Close()
		}
	})
	open := func() (net.Conn, error) {
		client, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return nil, err
		}
		_ = client.SetDeadline(time.Now().Add(time.Second))
		_, err = client.Write([]byte{5, 1, 2})
		var selected [2]byte
		if err == nil {
			_, err = io.ReadFull(client, selected[:])
		}
		if err != nil || selected != [2]byte{5, 2} {
			_ = client.Close()
			return nil, errors.New("bridge rejected pending handshake")
		}
		return client, nil
	}
	for range MaxBridgeConnections {
		client, err := open()
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
	}
	overflow, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer overflow.Close()
	assertBridgeClosed(t, overflow)
	_ = clients[0].Close()
	deadline := time.Now().Add(time.Second)
	for {
		client, err := open()
		if err == nil {
			clients = append(clients, client)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed handshake did not release bridge capacity")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.accepts.Load() != 0 {
		t.Fatalf("pending authentication dialed upstream %d times", p.accepts.Load())
	}
}

func TestBridgeHandshakeDeadlineClosesIncompleteRequest(t *testing.T) {
	p := newWirePeer(t)
	m := bridgeManager(t)
	outbound := Outbound{Tag: "deadline", Settings: p.config}
	stage, err := m.Prepare([]Outbound{outbound})
	if err != nil {
		t.Fatal(err)
	}
	stage.Commit()
	client := bridgeAuth(t, renderedBridge(t, m, outbound))
	_ = client.SetReadDeadline(time.Now().Add(6 * time.Second))
	var data [1]byte
	n, err := client.Read(data[:])
	var timeout net.Error
	if n != 0 || err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("incomplete request survived handshake deadline: %d %v", n, err)
	}
	if p.accepts.Load() != 0 {
		t.Fatalf("incomplete request reached upstream %d times", p.accepts.Load())
	}
}
