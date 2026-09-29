package httpupgrade

import (
	"bufio"
	"context"
	"errors"
	"io"
	gonet "net"
	"net/http"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
)

func TestHTTPUpgradeCancellationClosesPendingHandshake(t *testing.T) {
	listener, err := gonet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		conn, err := Dial(ctx, net.TCPDestination(net.LocalHostIP, net.Port(listener.Addr().(*gonet.TCPAddr).Port)), &internet.MemoryStreamConfig{ProtocolSettings: &Config{Path: "/"}})
		if conn != nil {
			_ = conn.Close()
		}
		finished <- err
	}()
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	request, err := http.ReadRequest(bufio.NewReader(peer))
	if err != nil {
		t.Fatal(err)
	}
	_ = request.Body.Close()
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, gonet.ErrClosed) {
			t.Fatalf("handshake cancellation returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled HTTPUpgrade handshake kept its dial alive")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("cancelled HTTPUpgrade left a live socket: %v", err)
	}
}
