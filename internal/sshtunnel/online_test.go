package sshtunnel

import (
	"cmp"
	"context"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func waitOnlineSessions(t *testing.T, server *Server, want []OnlineSession) {
	t.Helper()
	less := func(a, b OnlineSession) int {
		if a.Username != b.Username {
			return cmp.Compare(a.Username, b.Username)
		}
		return a.SourceIP.Compare(b.SourceIP)
	}
	slices.SortFunc(want, less)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := server.OnlineSessions()
		slices.SortFunc(got, less)
		if slices.Equal(got, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("admitted SSH sessions = %+v, want %+v", server.OnlineSessions(), want)
}

func TestOnlineSessionsRequirePolicyAdmissionAndKeepClientIdentity(t *testing.T) {
	db, _, controller, first := tunnelDB(t)
	second := model.ClientRecord{Email: "second", Enable: true}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: second.Email, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := controller.Configure(context.Background(), second.PolicyID, policyflow.Rates{}); err != nil {
		t.Fatal(err)
	}
	host, _ := tunnelKey(t)
	key, _ := tunnelKey(t)
	admission := make(chan struct{})
	gate := make(chan struct{})
	clients := []Client{
		{PolicyID: first.PolicyID, Username: "first", PublicKeys: []ssh.PublicKey{key.PublicKey()}},
		{PolicyID: second.PolicyID, Username: "second", PublicKeys: []ssh.PublicKey{key.PublicKey()}},
	}
	server, err := NewServer(Config{InboundTag: "presence", HostKey: host, Clients: clients, Authenticated: func(ctx context.Context, policyID string) error {
		if policyID == first.PolicyID {
			select {
			case admission <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-gate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}, controller, tcpTestDial)
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
	unsigned, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer unsigned.Close()
	waitOnlineSessions(t, server, nil)
	dial := func(username, source string) *ssh.Client {
		t.Helper()
		raw, err := (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}, Timeout: time.Second}).Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn, channels, requests, err := ssh.NewClientConn(raw, listener.Addr().String(), &ssh.ClientConfig{User: username, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey())})
		if err != nil {
			_ = raw.Close()
			t.Fatal(err)
		}
		client := ssh.NewClient(conn, channels, requests)
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	firstConn := dial("first", "127.0.0.1")
	select {
	case <-admission:
	case <-time.After(time.Second):
		t.Fatal("signed SSH session did not reach admission")
	}
	waitOnlineSessions(t, server, nil)
	close(gate)
	a := OnlineSession{PolicyID: first.PolicyID, Username: "first", SourceIP: netip.MustParseAddr("127.0.0.1")}
	b := OnlineSession{PolicyID: second.PolicyID, Username: "second", SourceIP: a.SourceIP}
	c := b
	c.SourceIP = netip.MustParseAddr("127.0.0.2")
	waitOnlineSessions(t, server, []OnlineSession{a})
	dial("second", "127.0.0.1")
	dial("second", "127.0.0.1")
	dial("second", "127.0.0.2")
	waitOnlineSessions(t, server, []OnlineSession{a, b, b, c})
	copyOfSnapshot := server.OnlineSessions()
	for i := range copyOfSnapshot {
		copyOfSnapshot[i] = OnlineSession{}
	}
	if reflect.DeepEqual(server.OnlineSessions(), copyOfSnapshot) {
		t.Fatal("a caller modified live session identity through the snapshot")
	}
	_ = firstConn.Close()
	waitOnlineSessions(t, server, []OnlineSession{b, b, c})
	if err := server.UpdateClients(clients[:1]); err != nil {
		t.Fatal(err)
	}
	waitOnlineSessions(t, server, nil)
	server.Close()
	waitOnlineSessions(t, server, nil)
}
