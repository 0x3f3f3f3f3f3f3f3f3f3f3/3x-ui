package websocket

import (
	"context"
	"errors"
	"io"
	gonet "net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ws "github.com/gorilla/websocket"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
)

func TestDelayedWebSocketCloseRejectsLateHandshake(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	adapter := &pausedWebSocketAddressDialer{entered: entered, release: release}
	internet.UseAlternativeSystemDialer(internet.WithAdapter(adapter))
	defer internet.UseAlternativeSystemDialer(nil)
	defer unblock()
	accepted := make(chan *ws.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&ws.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			accepted <- conn
		}
	}))
	defer server.Close()
	address := server.Listener.Addr().(*gonet.TCPAddr)
	conn, err := Dial(context.Background(), net.TCPDestination(net.LocalHostIP, net.Port(address.Port)), &internet.MemoryStreamConfig{ProtocolSettings: &Config{Ed: 128, Path: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	written := make(chan error, 1)
	go func() { _, err := conn.Write([]byte("early data")); written <- err }()
	var peer *ws.Conn
	select {
	case peer = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("real WebSocket handshake did not finish")
	}
	defer peer.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handshake publication barrier was not reached")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-written; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed deferred connection published a late handshake: %v", err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = peer.ReadMessage()
	var timeout gonet.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("late WebSocket socket remained open: %v", err)
	}
}

type pausedWebSocketAddressDialer struct{ entered, release chan struct{} }

func (d *pausedWebSocketAddressDialer) Dial(network, address string) (gonet.Conn, error) {
	conn, err := gonet.Dial(network, address)
	if err != nil {
		return nil, err
	}
	return &pausedWebSocketAddress{Conn: conn, entered: d.entered, release: d.release}, nil
}

type pausedWebSocketAddress struct {
	gonet.Conn
	once             sync.Once
	entered, release chan struct{}
}

func (c *pausedWebSocketAddress) RemoteAddr() gonet.Addr {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Conn.RemoteAddr()
}

func TestDeferredWebSocketDeadlineBeforeHandshakeReturnsError(t *testing.T) {
	for _, method := range []string{"all", "read", "write"} {
		t.Run(method, func(t *testing.T) {
			conn, err := Dial(context.Background(), net.TCPDestination(net.LocalHostIP, 1), &internet.MemoryStreamConfig{ProtocolSettings: &Config{Ed: 128}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			switch method {
			case "all":
				err = conn.SetDeadline(time.Now())
			case "read":
				err = conn.SetReadDeadline(time.Now())
			case "write":
				err = conn.SetWriteDeadline(time.Now())
			}
			if err == nil || !strings.HasSuffix(err.Error(), "WebSocket handshake has not completed") {
				t.Fatalf("unset deferred socket deadline returned %v", err)
			}
		})
	}
}
