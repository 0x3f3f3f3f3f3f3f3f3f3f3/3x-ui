package service

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

type passwordHotUDP struct {
	control net.Conn
	packet  net.PacketConn
	relay   *net.UDPAddr
}

func passwordHotAuthenticate(t *testing.T, port int, user, password string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	var response [2]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil || response != [2]byte{5, 2} {
		t.Fatalf("SOCKS5 negotiation: %v %v", response, err)
	}
	auth := append([]byte{1, byte(len(user))}, []byte(user)...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, []byte(password)...)
	if _, err := conn.Write(auth); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, response[:]); err != nil || response != [2]byte{1, 0} {
		t.Fatalf("SOCKS5 authentication: %v %v", response, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

func passwordHotAssociate(t *testing.T, port int, user, password string) *passwordHotUDP {
	t.Helper()
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packet.Close() })
	control := passwordHotAuthenticate(t, port, user, password)
	_ = control.SetDeadline(time.Now().Add(2 * time.Second))
	source := packet.LocalAddr().(*net.UDPAddr).Port
	if _, err := control.Write([]byte{5, 3, 0, 1, 127, 0, 0, 1, byte(source >> 8), byte(source)}); err != nil {
		t.Fatal(err)
	}
	var response [10]byte
	if _, err := io.ReadFull(control, response[:]); err != nil || !bytes.Equal(response[:4], []byte{5, 0, 0, 1}) {
		t.Fatalf("SOCKS5 UDP association: %x %v", response, err)
	}
	_ = control.SetDeadline(time.Time{})
	return &passwordHotUDP{control: control, packet: packet, relay: &net.UDPAddr{IP: net.IP(response[4:8]), Port: int(response[8])<<8 | int(response[9])}}
}

func passwordHotUDPEcho(t *testing.T, association *passwordHotUDP, target int) {
	t.Helper()
	frame := append([]byte{0, 0, 0, 1, 127, 0, 0, 1, byte(target >> 8), byte(target)}, []byte("hello!")...)
	_ = association.packet.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := association.packet.WriteTo(frame, association.relay); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 128)
	n, _, err := association.packet.ReadFrom(response)
	if err != nil || n != 16 || !bytes.Equal(response[:10], frame[:10]) || string(response[10:n]) != "hello!" {
		t.Fatalf("UDP payload: length=%d error=%v", n, err)
	}
}

func passwordHotUDPClosed(t *testing.T, association *passwordHotUDP) {
	t.Helper()
	managedActivationClosed(t, association.control)
	deadline := time.Now().Add(time.Second)
	for {
		probe, err := net.ListenPacket("udp4", association.relay.String())
		if err == nil {
			_ = probe.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("revoked association retained its UDP listener: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}
