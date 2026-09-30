package socks_test

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/proxy/socks"
)

func TestTempUDPConnCloseBeforeTimeout(t *testing.T) {
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	control, peer := net.Pipe()
	t.Cleanup(func() { packet.Close(); control.Close(); peer.Close() })
	conn := socks.NewTempUDPConn(packet, control, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	// A late handshake must not reopen the timer or the association.
	conn.SetTimeout(time.Hour)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("associated TCP connection stayed open")
	}
	replacement, err := net.ListenPacket("udp4", packet.LocalAddr().String())
	if err != nil {
		t.Fatalf("UDP port stayed occupied: %v", err)
	}
	replacement.Close()
}

func TestTempUDPConnConcurrentTimeoutAndClose(t *testing.T) {
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	control, peer := net.Pipe()
	t.Cleanup(func() { packet.Close(); control.Close(); peer.Close() })
	conn := socks.NewTempUDPConn(packet, control, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 20 {
				conn.SetTimeout(time.Hour)
				conn.Close()
			}
		}()
	}
	workers.Wait()
}
