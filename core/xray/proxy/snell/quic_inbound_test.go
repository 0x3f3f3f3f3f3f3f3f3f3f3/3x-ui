package snell

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/transport/pipe"
)

type quicDatagramTestConn struct {
	reader *pipe.Reader
	writer *pipe.Writer
	once   sync.Once
}

func newQUICDatagramTestConn() *quicDatagramTestConn {
	r, w := pipe.New()
	return &quicDatagramTestConn{reader: r, writer: w}
}

func (c *quicDatagramTestConn) ReadMultiBuffer() (buf.MultiBuffer, error) {
	return c.reader.ReadMultiBuffer()
}

func (*quicDatagramTestConn) Read([]byte) (int, error)    { panic("datagram reader required") }
func (*quicDatagramTestConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *quicDatagramTestConn) Close() error {
	c.once.Do(func() { c.reader.Interrupt(); c.writer.Close() })
	return nil
}

func (*quicDatagramTestConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7177}
}

func (*quicDatagramTestConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 30000}
}
func (*quicDatagramTestConn) SetDeadline(time.Time) error      { return nil }
func (*quicDatagramTestConn) SetReadDeadline(time.Time) error  { return nil }
func (*quicDatagramTestConn) SetWriteDeadline(time.Time) error { return nil }

func quicTestInbound(t *testing.T) *Inbound {
	t.Helper()
	i, err := NewServer(context.Background(), &ServerConfig{Version: 5, User: &protocol.User{ClientId: "d395b8b1-31ab-47ea-9a67-e46dcc84cd96", Email: "quic-owner", Account: serial.ToTypedMessage(&Account{Psk: "native-quic-test-secret"})}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { i.Close() })
	return i
}

func TestNativeSnellQUICUnauthenticatedHandshakeBound(t *testing.T) {
	i := quicTestInbound(t)
	instance := new(core.Instance)
	if err := instance.AddFeature(timeoutPolicy{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
	c := newQUICDatagramTestConn()
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- i.Process(ctx, X.Network_UDP, c, nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unauthenticated UDP session succeeded")
		}
	case <-time.After(500 * time.Millisecond):
		i.Close()
		<-done
		t.Fatal("no-op UDP deadlines bypassed handshake timeout")
	}
	i.mu.RLock()
	active := len(i.physical)
	i.mu.RUnlock()
	if active != 0 {
		t.Fatal("handshake timeout retained a physical association")
	}
}

func TestNativeSnellQUICPhysicalCapacityAndCloseJoin(t *testing.T) {
	i := quicTestInbound(t)
	done := make(chan error, 129)
	for range 128 {
		c := newQUICDatagramTestConn()
		go func() { done <- i.Process(context.Background(), X.Network_UDP, c, nil) }()
	}
	deadline := time.Now().Add(time.Second)
	for {
		i.mu.RLock()
		active := len(i.physical)
		i.mu.RUnlock()
		if active == 128 {
			break
		}
		if time.Now().After(deadline) {
			i.Close()
			t.Fatalf("physical capacity did not admit available slots: %d", active)
		}
		time.Sleep(time.Millisecond)
	}
	overflow := newQUICDatagramTestConn()
	defer overflow.Close()
	if err := i.Process(context.Background(), X.Network_UDP, overflow, nil); err == nil {
		t.Fatal("QUIC physical source associations exceeded128")
	}
	i.Close()
	for range 128 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("handler close failed to join UDP reads")
		}
	}
	i.mu.RLock()
	active := len(i.physical)
	i.mu.RUnlock()
	if active != 0 {
		t.Fatalf("closed QUIC retained physical associations: %d", active)
	}
}
