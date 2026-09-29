package outbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	gonet "net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestOutboundCloseEndsActiveDialAndRejectsNewDial(t *testing.T) {
	listener, err := gonet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	target := net.TCPDestination(net.LocalHostIP, net.Port(listener.Addr().(*gonet.TCPAddr).Port))
	accepted := make(chan gonet.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	handler := &Handler{}
	conn, err := handler.Dial(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	peer := <-accepted
	t.Cleanup(func() { _ = peer.Close() })
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	_, err = peer.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("handler close retained an active socket: %v", err)
	}
	again, err := handler.Dial(context.Background(), target)
	if again != nil {
		_ = again.Close()
	}
	if !errors.Is(err, gonet.ErrClosed) {
		t.Fatalf("closed handler dial = %v, want net.ErrClosed", err)
	}
}

func TestClosedOutboundRejectsInvalidDialBeforeTransport(t *testing.T) {
	handler := &Handler{streamSettings: &internet.MemoryStreamConfig{ProtocolName: "invalid-closed-handler"}}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	conn, err := handler.Dial(context.Background(), net.TCPDestination(net.LocalHostIP, 1))
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, gonet.ErrClosed) {
		t.Fatalf("closed handler reached transport: %v", err)
	}
}

func TestOutboundCloseCancelsDispatchBeforeItDials(t *testing.T) {
	started, stopped, abort := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(abort) })
	handler := &Handler{proxy: &waitingOutboundProxy{started: started, abort: abort}}
	reader, writer := pipe.New()
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: net.TCPDestination(net.LocalHostIP, 1)}})
	go func() { handler.Dispatch(ctx, &transport.Link{Reader: reader, Writer: writer}); close(stopped) }()
	<-started
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("handler close left pre-dial dispatch alive")
	}
}

type waitingOutboundProxy struct{ started, abort chan struct{} }

func (p *waitingOutboundProxy) Process(ctx context.Context, _ *transport.Link, _ internet.Dialer) error {
	close(p.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.abort:
		return nil
	}
}

var transportNumber atomic.Uint64

func TestOutboundCloseCancelsPendingDialAndDiscardsLateConnection(t *testing.T) {
	for _, waitForCancel := range []bool{true, false} {
		t.Run(fmt.Sprint(waitForCancel), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			local, peer := gonet.Pipe()
			t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
			protocol := fmt.Sprintf("test-close-dial-%d", transportNumber.Add(1))
			err := internet.RegisterTransportDialer(protocol, func(ctx context.Context, _ net.Destination, _ *internet.MemoryStreamConfig) (stat.Connection, error) {
				close(entered)
				if waitForCancel {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-release:
						return nil, io.EOF
					}
				}
				<-release
				return local, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			handler := &Handler{streamSettings: &internet.MemoryStreamConfig{ProtocolName: protocol}}
			result := make(chan error, 1)
			go func() {
				conn, err := handler.Dial(context.Background(), net.TCPDestination(net.LocalHostIP, 1))
				if conn != nil {
					_ = conn.Close()
				}
				result <- err
			}()
			<-entered
			if err := handler.Close(); err != nil {
				t.Fatal(err)
			}
			if !waitForCancel {
				unblock()
			}
			select {
			case err := <-result:
				if !errors.Is(err, gonet.ErrClosed) && !errors.Is(err, context.Canceled) {
					t.Fatalf("closed handler returned live/incorrect dial result: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("pending dial survived handler close")
			}
			if !waitForCancel {
				_, err := peer.Read(make([]byte, 1))
				if !errors.Is(err, io.EOF) {
					t.Fatalf("late connection remained open: %v", err)
				}
			}
		})
	}
}

func TestOutboundNormalSocketCloseReleasesOwnerRecords(t *testing.T) {
	listener, err := gonet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	handler := &Handler{}
	defer handler.Close()
	target := net.TCPDestination(net.LocalHostIP, net.Port(listener.Addr().(*gonet.TCPAddr).Port))
	conn, err := handler.Dial(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	handler.lifecycleAccess.Lock()
	connections, tasks := len(handler.connections), len(handler.tasks)
	handler.lifecycleAccess.Unlock()
	if connections != 0 || tasks != 0 {
		t.Fatalf("ordinary connection completion retained owner records: connections=%d tasks=%d", connections, tasks)
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("closed connection was closed again: %v", err)
	}
}

func TestClosedOutboundRejectsPreviouslySelectedDispatch(t *testing.T) {
	started, abort := make(chan struct{}), make(chan struct{})
	defer close(abort)
	handler := &Handler{proxy: &waitingOutboundProxy{started: started, abort: abort}}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	reader, writer := pipe.New()
	defer reader.Interrupt()
	defer writer.Close()
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: net.TCPDestination(net.LocalHostIP, 1)}})
	stopped := make(chan struct{})
	go func() { handler.Dispatch(ctx, &transport.Link{Reader: reader, Writer: writer}); close(stopped) }()
	select {
	case <-started:
		t.Fatal("previously selected handler admitted proxy traffic after Close")
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("closed dispatch left its link pending")
	}
	buffer := buf.New()
	_, _ = buffer.Write([]byte("late data"))
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{buffer}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed dispatch left writer usable: %v", err)
	}
}
