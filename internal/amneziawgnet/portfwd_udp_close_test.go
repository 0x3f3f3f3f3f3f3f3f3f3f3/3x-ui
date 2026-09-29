package amneziawgnet

import (
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestPortForwardUDPCloseDuringTargetResolution(t *testing.T) {
	gs := newTestStack(t, "10.208.0.1")
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &udpForwardListener{pc: pc, sessions: map[netip.AddrPort]*udpForwardSession{}}
	t.Cleanup(listener.Close)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		listener.readLoop(gs, 801, portForwardKey{email: "closing-peer", port: 58944, proto: udpForward}, func(string) (netip.Addr, bool) {
			close(entered)
			<-release
			return netip.MustParseAddr("10.208.0.2"), true
		})
	}()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		listener.Close()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("UDP read loop survived owned cleanup")
		}
	})
	conn, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("close-race")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("real datagram did not reach target resolution")
	}
	listener.Close()
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("read loop survived closed listener")
	}
	listener.mu.Lock()
	count := len(listener.sessions)
	listener.mu.Unlock()
	if count != 0 {
		t.Fatalf("closed forward published %d tunnel sessions after shutdown", count)
	}
}
