package inbound

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	appstats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestUDPWorkerFreezeWaitsThroughIO(t *testing.T) {
	for _, writing := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[writing], func(t *testing.T) {
			manager, _ := appstats.NewManager(context.Background(), &appstats.Config{})
			counter, _ := manager.GetOrRegisterCounter("udp")
			reader, writer := pipe.New()
			t.Cleanup(func() { reader.Interrupt(); _ = writer.Close() })
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			conn := &udpConn{
				uplink: counter, downlink: counter,
				reader: &pausedUDPReader{Reader: reader, entered: entered, release: release},
				output: func(p []byte) (int, error) { close(entered); <-release; return len(p), nil },
			}
			operation := func() error {
				if writing {
					_, err := conn.Write([]byte("abc"))
					return err
				}
				mb, err := conn.ReadMultiBuffer()
				buf.ReleaseMulti(mb)
				return err
			}
			done := make(chan error, 1)
			go func() { done <- operation() }()
			if !writing {
				if err := writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("abc"))); err != nil {
					t.Fatal(err)
				}
			}
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			_, err := manager.SealCounters(ctx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("freeze overtook UDP worker accounting: %v", err)
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			final, err := manager.SealCounters(context.Background())
			if err != nil || final["udp"] != 3 {
				t.Fatalf("final UDP count: %v %v", final, err)
			}
			if err := operation(); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("frozen worker admitted IO: %v", err)
			}
		})
	}
}

type pausedUDPReader struct {
	buf.Reader
	entered chan struct{}
	release chan struct{}
}

func (r *pausedUDPReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.Reader.ReadMultiBuffer()
	close(r.entered)
	<-r.release
	return mb, err
}
