package amneziawgnet

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestPortForwardTCPCloseCancelsPendingTunnelDial(t *testing.T) {
	gs := newTestStack(t, "10.209.0.1")
	entered := make(chan struct{})
	listener := listenPortForwardTCP(gs, 802, portForwardKey{email: "pending-peer", port: 58945, proto: tcpForward}, func(string) (netip.Addr, bool) {
		close(entered)
		return netip.MustParseAddr("10.209.0.2"), true
	})
	if listener == nil {
		t.Fatal("open test TCP forward")
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:58945", time.Second)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		listener.Close()
		t.Fatal("accepted TCP connection did not attempt its tunnel target")
	}
	deadline := time.Now().Add(2 * time.Second)
	listener.Close()
	listener.Close()
	requireForwardTCPClosedBy(t, conn, deadline)
}

func requireForwardTCPClosedBy(t *testing.T, conn net.Conn, deadline time.Time) {
	t.Helper()
	_ = conn.SetReadDeadline(deadline)
	var one [1]byte
	n, err := conn.Read(one[:])
	if n != 0 || err == nil {
		t.Fatalf("removed forward still delivered TCP bytes: %d %v", n, err)
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("removed forward left its TCP connection open: %v", err)
	}
	if time.Now().After(deadline) {
		t.Fatal("forward closure exceeded its two-second deadline")
	}
}
