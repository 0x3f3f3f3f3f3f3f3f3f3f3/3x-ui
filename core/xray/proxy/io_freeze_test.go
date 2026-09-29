package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/dispatcher"
	appstats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/transport/internet/stat"
)

func TestVisionCounterFreezeWaitsForDirectIO(t *testing.T) {
	for _, path := range []string{"read", "write", "switch-writer"} {
		t.Run(path, func(t *testing.T) {
			manager, _ := appstats.NewManager(context.Background(), &appstats.Config{})
			counter, _ := manager.GetOrRegisterCounter(path)
			left, right := net.Pipe()
			t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
			paused := &pausedVisionConn{Conn: left, entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			unblock := func() { once.Do(func() { close(paused.release) }) }
			t.Cleanup(unblock)
			state := &TrafficState{}
			reader := &VisionReader{Reader: buf.NewReader(paused), trafficState: state, directReadCounter: counter}
			state.Outbound.DownlinkReaderDirectCopy = true
			writer := &VisionWriter{Writer: buf.NewWriter(paused), trafficState: state, directWriteCounter: counter, ctx: context.Background()}
			if path == "switch-writer" {
				writer.directWriteCounter = nil
				writer.conn = &stat.CounterConnection{Connection: paused, WriteCounter: counter}
				state.Inbound.DownlinkWriterDirectCopy = true
			}
			operation := func() error {
				if path == "read" {
					mb, err := reader.ReadMultiBuffer()
					buf.ReleaseMulti(mb)
					return err
				}
				return writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("abc")))
			}
			done := make(chan error, 1)
			go func() { done <- operation() }()
			_ = right.SetDeadline(time.Now().Add(2 * time.Second))
			if path == "read" {
				if _, err := right.Write([]byte("abc")); err != nil {
					t.Fatal(err)
				}
			} else if _, err := io.ReadFull(right, make([]byte, 3)); err != nil {
				t.Fatal(err)
			}
			<-paused.entered
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			_, err := manager.SealCounters(ctx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("freeze overtook Vision IO: %v", err)
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			final, err := manager.SealCounters(context.Background())
			if err != nil || final[path] != 3 {
				t.Fatalf("final Vision count: %v %v", final, err)
			}
			if err := operation(); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("frozen Vision admitted IO: %v", err)
			}
		})
	}
}

type pausedVisionConn struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *pausedVisionConn) pause() { c.once.Do(func() { close(c.entered); <-c.release }) }

func (c *pausedVisionConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.pause()
	return n, err
}

func (c *pausedVisionConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.pause()
	return n, err
}

func TestRawCopyFreezeWaitsForFinalCounters(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "android" {
		t.Skip("raw splice is Linux/Android only")
	}
	for _, splice := range []bool{false, true} {
		for _, guarded := range []string{"outbound", "inbound", "user"} {
			if !splice && guarded != "outbound" {
				continue
			}
			t.Run(map[bool]string{false: "buffered", true: "splice"}[splice]+"/"+guarded, func(t *testing.T) {
				manager, _ := appstats.NewManager(context.Background(), &appstats.Config{})
				counter, _ := manager.GetOrRegisterCounter(guarded)
				counters := map[string]*appstats.Counter{"outbound": {}, "inbound": {}, "user": {}}
				counters[guarded] = counter.(*appstats.Counter)
				source, sourcePeer := freezeTCPPair(t)
				destination, destinationPeer := freezeTCPPair(t)
				var inputConn net.Conn = source
				var waiting chan struct{}
				if !splice {
					waiting = make(chan struct{})
					inputConn = &secondReadConn{Conn: source, waiting: waiting}
				}
				input := &stat.CounterConnection{Connection: inputConn, ReadCounter: counters["outbound"]}
				output := &stat.CounterConnection{Connection: destination, WriteCounter: counters["inbound"]}
				writer := &dispatcher.SizeStatWriter{Counter: counters["user"], Writer: buf.NewWriter(output)}
				state := 2
				if splice {
					state = 1
				}
				ctx := session.ContextWithInbound(context.Background(), &session.Inbound{CanSpliceCopy: state})
				ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{CanSpliceCopy: state}})
				timer := signal.CancelAfterInactivity(ctx, func() {}, time.Minute)
				t.Cleanup(func() { timer.SetTimeout(0) })
				done := make(chan error, 1)
				go func() { done <- CopyRawConnIfExist(ctx, input, output, writer, timer, nil) }()
				if _, err := sourcePeer.Write([]byte("abc")); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(destinationPeer, make([]byte, 3)); err != nil {
					t.Fatal(err)
				}
				if waiting != nil {
					<-waiting
				}
				sealCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				_, err := manager.SealCounters(sealCtx)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("freeze overtook raw copy accounting: %v", err)
				}
				if err := sourcePeer.CloseWrite(); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
					t.Fatal(err)
				}
				final, err := manager.SealCounters(context.Background())
				if err != nil || final[guarded] != 3 {
					t.Fatalf("final raw count: %v %v", final, err)
				}
				for name, c := range counters {
					if c.Value() != 3 {
						t.Fatalf("%s counted %d, want 3", name, c.Value())
					}
				}
			})
		}
	}
}

type secondReadConn struct {
	net.Conn
	reads   int
	waiting chan struct{}
}

func (c *secondReadConn) Read(p []byte) (int, error) {
	c.reads++
	if c.reads == 2 {
		close(c.waiting)
	}
	return c.Conn.Read(p)
}

func freezeTCPPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	return server.(*net.TCPConn), client.(*net.TCPConn)
}

func TestRawSpliceFreezeRejectsLaterCounterAndReleasesEarlier(t *testing.T) {
	for _, sealed := range []int{1, 2} {
		t.Run(map[int]string{1: "inbound", 2: "user"}[sealed], func(t *testing.T) {
			first, _ := appstats.NewManager(context.Background(), &appstats.Config{})
			last, _ := appstats.NewManager(context.Background(), &appstats.Config{})
			open, _ := first.GetOrRegisterCounter("open")
			closed, _ := last.GetOrRegisterCounter("closed")
			if _, err := last.SealCounters(context.Background()); err != nil {
				t.Fatal(err)
			}
			counters := [3]*appstats.Counter{open.(*appstats.Counter), open.(*appstats.Counter), open.(*appstats.Counter)}
			counters[sealed] = closed.(*appstats.Counter)
			writer, _ := freezeTCPPair(t)
			reader := &partialRawConn{err: io.EOF}
			err := copyRawSplice(writer, reader, counters[0], counters[1], counters[2])
			if !errors.Is(err, net.ErrClosed) || reader.read {
				t.Fatalf("splice read before acquiring every counter: %v, read=%v", err, reader.read)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			final, err := first.SealCounters(ctx)
			if err != nil || final["open"] != 0 {
				t.Fatalf("partial splice admission leaked leases: %v %v", final, err)
			}
		})
	}
}

func TestRawSpliceAccountsPartialTransferBeforeError(t *testing.T) {
	manager, _ := appstats.NewManager(context.Background(), &appstats.Config{})
	up, _ := manager.GetOrRegisterCounter("outbound")
	down, _ := manager.GetOrRegisterCounter("inbound")
	user, _ := manager.GetOrRegisterCounter("user")
	writer, peer := freezeTCPPair(t)
	wantErr := errors.New("source failed after payload")
	err := copyRawSplice(writer, &partialRawConn{err: wantErr}, up, down, user)
	if !errors.Is(err, wantErr) {
		t.Fatalf("lost source error: %v", err)
	}
	if _, err := io.ReadFull(peer, make([]byte, 3)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	final, err := manager.SealCounters(ctx)
	if err != nil || final["outbound"] != 3 || final["inbound"] != 3 || final["user"] != 3 {
		t.Fatalf("partial copy counters: %v %v", final, err)
	}
}

type partialRawConn struct {
	net.Conn
	err  error
	read bool
}

func (c *partialRawConn) Read(p []byte) (int, error) {
	c.read = true
	return copy(p, "abc"), c.err
}
