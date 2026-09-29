package stats_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	appstats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/proxy/freedom"
	"github.com/xtls/xray-core/proxy/wireguard"
	"github.com/xtls/xray-core/transport/internet"
)

func TestPacketCounterFreezeWaitsForCompletedDatagram(t *testing.T) {
	for _, path := range []string{"freedom-read", "freedom-write", "wireguard-read", "wireguard-write"} {
		t.Run(path, func(t *testing.T) {
			manager, _ := appstats.NewManager(context.Background(), &appstats.Config{})
			counter, _ := manager.GetOrRegisterCounter(path)
			left, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = left.Close() })
			right, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = right.Close() })
			_ = left.SetDeadline(time.Now().Add(2 * time.Second))
			_ = right.SetDeadline(time.Now().Add(2 * time.Second))
			paused := &pausedAccountingPacket{PacketConn: left, completed: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			unblock := func() { once.Do(func() { close(paused.release) }) }
			t.Cleanup(unblock)
			packet := &internet.PacketConnWrapper{PacketConn: paused, Dest: right.LocalAddr()}
			wg := &wireguard.PacketCounterConnection{PacketConn: paused, ReadCounter: counter, WriteCounter: counter}
			reader := &freedom.PacketReader{PacketConnWrapper: packet, Counter: counter, Handler: &freedom.Handler{}}
			writer := &freedom.PacketWriter{PacketConnWrapper: packet, Counter: counter}
			operation := func() error {
				switch path {
				case "freedom-read":
					mb, err := reader.ReadMultiBuffer()
					buf.ReleaseMulti(mb)
					return err
				case "freedom-write":
					return writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("abc")))
				case "wireguard-read":
					_, _, err := wg.ReadFrom(make([]byte, 16))
					return err
				default:
					_, err := wg.WriteTo([]byte("abc"), right.LocalAddr())
					return err
				}
			}
			done := make(chan error, 1)
			go func() { done <- operation() }()
			if path == "freedom-read" || path == "wireguard-read" {
				if _, err := right.WriteTo([]byte("abc"), left.LocalAddr()); err != nil {
					t.Fatal(err)
				}
			} else if n, _, err := right.ReadFrom(make([]byte, 16)); err != nil || n != 3 {
				t.Fatalf("datagram delivery: %d %v", n, err)
			}
			<-paused.completed
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			_, err = manager.SealCounters(ctx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("freeze overtook datagram accounting: %v", err)
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			final, err := manager.SealCounters(context.Background())
			if err != nil || final[path] != 3 {
				t.Fatalf("final datagram count: %v %v", final, err)
			}
			if err := operation(); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("frozen datagram IO was admitted: %v", err)
			}
		})
	}
}

type pausedAccountingPacket struct {
	net.PacketConn
	completed chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (c *pausedAccountingPacket) pause() {
	c.once.Do(func() { close(c.completed); <-c.release })
}

func (c *pausedAccountingPacket) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	c.pause()
	return n, addr, err
}

func (c *pausedAccountingPacket) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, addr)
	c.pause()
	return n, err
}
