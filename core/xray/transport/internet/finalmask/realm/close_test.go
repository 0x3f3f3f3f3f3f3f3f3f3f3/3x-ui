package realm

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestRealmCloseReleasesUnderlyingPacketSocket(t *testing.T) {
	raw, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	address := raw.LocalAddr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &realmConnClient{PacketConn: raw, ctx: ctx, cancel: cancel}
	reading, stopped := make(chan struct{}), make(chan error, 1)
	go func() { close(reading); _, _, err := conn.ReadFrom(make([]byte, 32)); stopped <- err }()
	<-reading
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("packet shutdown returned %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("realm Close left its packet reader and socket alive")
	}
	replacement, err := net.ListenPacket("udp", address)
	if err != nil {
		t.Fatalf("closed realm socket retained its local port: %v", err)
	}
	defer replacement.Close()
	if err := conn.Close(); err != nil {
		t.Fatalf("repeat realm Close: %v", err)
	}
}
