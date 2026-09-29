package splithttp

import (
	"context"
	gotls "crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/tls"
)

func TestXHTTPStreamCloseCancelsUnansweredResponse(t *testing.T) {
	entered, stopped, abort := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-abort:
		}
	}))
	defer server.Close()
	defer close(abort)
	client := &DefaultDialerClient{transportConfig: &Config{}, client: server.Client()}
	stream, _, _, err := client.OpenStream(context.Background(), server.URL, "session", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	<-entered
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("XHTTP logical close left an unanswered HTTP request alive")
	}
}

func TestXHTTPOpenStreamCancellationReleasesPendingDial(t *testing.T) {
	entered, finished, abort := make(chan struct{}), make(chan error, 1), make(chan struct{})
	defer close(abort)
	exited := make(chan struct{})
	internet.UseAlternativeSystemDialer(&blockedXHTTPSystemDialer{entered: entered, exited: exited, abort: abort})
	defer internet.UseAlternativeSystemDialer(nil)
	client := createHTTPClient(xnet.TCPDestination(xnet.LocalHostIP, 80), &internet.MemoryStreamConfig{ProtocolSettings: &Config{}}).(*DefaultDialerClient)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		stream, _, _, err := client.OpenStream(ctx, "http://pending.example", "session", nil, false)
		if stream != nil {
			_ = stream.Close()
		}
		finished <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled stream dial returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("XHTTP stream ignored cancellation while waiting for its socket")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("underlying XHTTP dial still runs after its sole request was canceled")
	}
}

func TestXHTTPPacketCancellationClosesUnansweredRequest(t *testing.T) {
	entered, stopped, abort := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-abort:
		}
	}))
	defer server.Close()
	defer close(abort)
	client := &DefaultDialerClient{transportConfig: &Config{}, client: server.Client(), httpVersion: "2"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		payload := buf.New()
		_, _ = payload.Write([]byte("packet"))
		finished <- client.PostPacket(ctx, server.URL, "session", "0", buf.MultiBuffer{payload})
	}()
	<-entered
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled packet request returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("XHTTP packet request ignored cancellation while waiting for response")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cancelled XHTTP packet request left its server socket active")
	}
	if client.IsClosed() {
		t.Fatal("canceling one request marked its shared HTTP transport unhealthy")
	}
}

func TestXHTTPDialRetainsDetachedLifetimeUntilConnectionClose(t *testing.T) {
	entered, reply, stopped, abort := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		go func() { _, _ = io.Copy(io.Discard, r.Body) }()
		close(entered)
		select {
		case <-r.Context().Done():
			close(stopped)
			return
		case <-abort:
			return
		case <-reply:
		}
		_, _ = w.Write([]byte("alive"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-abort:
		}
	}))
	defer server.Close()
	defer close(abort)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	address := server.Listener.Addr().(*net.TCPAddr)
	conn, err := Dial(ctx, xnet.TCPDestination(xnet.LocalHostIP, xnet.Port(address.Port)), &internet.MemoryStreamConfig{ProtocolSettings: &Config{Mode: "stream-one", Path: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	select {
	case <-stopped:
		t.Fatal("caller cancellation ended an established detached XHTTP connection")
	case <-time.After(20 * time.Millisecond):
	}
	close(reply)
	data := make([]byte, 5)
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != "alive" {
		t.Fatalf("established stream lost its detached lifetime: %q %v", data, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("XHTTP Close did not end its detached HTTP request")
	}
}

func TestXHTTPHTTP1PacketCancellationStopsPendingIO(t *testing.T) {
	for _, phase := range []string{"dial", "write", "response"} {
		t.Run(phase, func(t *testing.T) {
			entered, abort := make(chan struct{}), make(chan struct{})
			defer close(abort)
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			client := &DefaultDialerClient{transportConfig: &Config{}, httpVersion: "1.1", uploadRawPool: &sync.Pool{}}
			observed := &observedHTTP1Conn{Conn: local, entered: entered, phase: phase}
			client.dialUploadConn = func(ctx context.Context) (net.Conn, error) {
				if phase == "dial" {
					close(entered)
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-abort:
						return nil, io.ErrClosedPipe
					}
				}
				return observed, nil
			}
			if phase == "response" {
				cached := NewH1Conn(observed)
				cached.UnreadedResponsesCount = 1
				client.uploadRawPool.New = func() any { return cached }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopped := make(chan error, 1)
			go func() {
				payload := buf.New()
				_, _ = payload.Write([]byte("packet"))
				stopped <- client.PostPacket(ctx, "http://pending.example", "session", "0", buf.MultiBuffer{payload})
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("packet did not enter pending IO")
			}
			cancel()
			select {
			case err := <-stopped:
				if !errors.Is(err, context.Canceled) && !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("cancelled HTTP/1.1 %s returned %v", phase, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("HTTP/1.1 %s ignored connection cancellation", phase)
			}
			if client.IsClosed() {
				t.Fatal("canceling HTTP/1.1 packet marked its shared client unhealthy")
			}
		})
	}
}

type observedHTTP1Conn struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
	phase   string
}

func (c *observedHTTP1Conn) Write(p []byte) (int, error) {
	if c.phase == "write" {
		c.once.Do(func() { close(c.entered) })
	}
	return c.Conn.Write(p)
}

func (c *observedHTTP1Conn) Read(p []byte) (int, error) {
	if c.phase == "response" {
		c.once.Do(func() { close(c.entered) })
	}
	return c.Conn.Read(p)
}

type blockedXHTTPSystemDialer struct{ entered, exited, abort chan struct{} }

func (d *blockedXHTTPSystemDialer) DestIpAddress() xnet.IP { return nil }
func (d *blockedXHTTPSystemDialer) Dial(ctx context.Context, _ xnet.Address, _ xnet.Destination, _ *internet.SocketConfig) (net.Conn, error) {
	close(d.entered)
	defer close(d.exited)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.abort:
		return nil, io.ErrClosedPipe
	}
}

func TestXHTTPClientCloseStopsPendingDials(t *testing.T) {
	for _, version := range []string{"1.1", "2", "3"} {
		t.Run(version, func(t *testing.T) {
			entered, exited, abort := make(chan struct{}), make(chan struct{}), make(chan struct{})
			internet.UseAlternativeSystemDialer(&blockedXHTTPSystemDialer{entered: entered, exited: exited, abort: abort})
			defer internet.UseAlternativeSystemDialer(nil)
			defer close(abort)
			client := createHTTPClient(xnet.TCPDestination(xnet.LocalHostIP, 443), &internet.MemoryStreamConfig{
				ProtocolSettings: &Config{}, SecurityType: "tls", SecuritySettings: &tls.Config{NextProtocol: []string{map[string]string{"1.1": "http/1.1", "2": "h2", "3": "h3"}[version]}},
			}).(*DefaultDialerClient)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				stream, _, _, err := client.OpenStream(ctx, "https://127.0.0.1", "session", nil, false)
				if stream != nil {
					_ = stream.Close()
				}
				finished <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("dial did not start")
			}
			closed := make(chan struct{})
			go func() { _ = client.Close(); close(closed) }()
			select {
			case <-exited:
			case <-time.After(time.Second):
				t.Fatal("client Close left an underlying dial running")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("client Close did not finish")
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("closed client returned a stream")
				}
			case <-time.After(time.Second):
				t.Fatal("request did not finish")
			}
		})
	}
}

func TestXHTTPUnhealthyClientStillServesExistingPacketStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &DefaultDialerClient{transportConfig: &Config{}, client: server.Client(), httpVersion: "2"}
	client.closed.Store(true)
	payload := buf.New()
	_, _ = payload.Write([]byte("existing session"))
	if err := client.PostPacket(context.Background(), server.URL, "existing", "1", buf.MultiBuffer{payload}); err != nil {
		t.Fatalf("unreusable client rejected existing stream: %v", err)
	}
}

func TestXHTTPClientCloseStopsRawPacketWrite(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	entered := make(chan struct{})
	client := &DefaultDialerClient{transportConfig: &Config{}, httpVersion: "1.1", client: &http.Client{}, uploadRawPool: &sync.Pool{}}
	client.dialUploadConn = func(context.Context) (net.Conn, error) {
		return &observedHTTP1Conn{Conn: local, entered: entered, phase: "write"}, nil
	}
	finished := make(chan error, 1)
	go func() {
		payload := buf.New()
		_, _ = payload.Write([]byte("packet"))
		finished <- client.PostPacket(context.Background(), "http://pending.example", "session", "0", buf.MultiBuffer{payload})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("raw packet write did not start")
	}
	_ = client.Close()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("closed client completed blocked write")
		}
	case <-time.After(time.Second):
		t.Fatal("client Close retained a raw packet write")
	}
	client.dialUploadConn = func(context.Context) (net.Conn, error) {
		t.Error("closed client reached its dialer")
		return nil, net.ErrClosed
	}
	if err := client.PostPacket(context.Background(), "http://pending.example", "session", "1", nil); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed client admitted packet: %v", err)
	}
}

func TestXHTTPSharedHTTP3DialSurvivesFirstRequestCancellation(t *testing.T) {
	client, url := newXHTTP3CancellationClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("alive")) }))
	transport := client.client.Transport.(*http3.Transport)
	entered, release, second := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	dial := transport.Dial
	transport.Dial = func(ctx context.Context, addr string, cfg *gotls.Config, qcfg *quic.Config) (*quic.Conn, error) {
		close(entered)
		<-release
		return dial(ctx, addr, cfg, qcfg)
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	resultA, resultB := make(chan error, 1), make(chan error, 1)

	go func() {
		stream, _, _, err := client.OpenStream(ctxA, url, "a", nil, false)
		if stream != nil {
			_ = stream.Close()
		}
		resultA <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first request did not start shared QUIC dial")
	}
	ctxB, cancelB := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelB()
	ctxB = httptrace.WithClientTrace(ctxB, &httptrace.ClientTrace{GetConn: func(string) { close(second) }})
	go func() {
		stream, _, _, err := client.OpenStream(ctxB, url, "b", nil, false)
		if err == nil {
			defer stream.Close()
			var data []byte
			data, err = io.ReadAll(stream)
			if err == nil && string(data) != "alive" {
				err = fmt.Errorf("unexpected body %q", data)
			}
		}
		resultB <- err
	}()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("second request did not join pending QUIC dial")
	}
	cancelA()
	select {
	case err := <-resultA:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not cancel")
	}
	unblock()
	select {
	case err := <-resultB:
		if err != nil {
			t.Fatalf("unrelated second request failed: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("second request did not finish")
	}
	if client.IsClosed() {
		t.Fatal("request cancellation poisoned shared HTTP/3 client")
	}
}

func newXHTTP3CancellationClient(t *testing.T, handler http.Handler) (*DefaultDialerClient, string) {
	t.Helper()
	tlsFixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(tlsFixture.Close)
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http3.Server{TLSConfig: tlsFixture.TLS.Clone(), Handler: handler}
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); _ = server.Serve(packet) }()
	t.Cleanup(func() { _ = server.Close(); _ = packet.Close(); <-serverDone })
	addr := packet.LocalAddr().(*net.UDPAddr)
	client := createHTTPClient(xnet.UDPDestination(xnet.LocalHostIP, xnet.Port(addr.Port)), &internet.MemoryStreamConfig{
		ProtocolSettings: &Config{}, SecurityType: "tls", SecuritySettings: &tls.Config{NextProtocol: []string{"h3"}},
		QuicParams: &internet.QuicParams{DisableChromeParrot: true, Congestion: "reno"},
	}).(*DefaultDialerClient)
	transport := client.client.Transport.(*http3.Transport)
	transport.TLSClientConfig = &gotls.Config{InsecureSkipVerify: true}
	t.Cleanup(func() { _ = client.Close() })

	return client, fmt.Sprintf("https://127.0.0.1:%d", addr.Port)
}

func TestXHTTPRetiredHTTP3DialPreservesUploadBody(t *testing.T) {
	client, url := newXHTTP3CancellationClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != "upload survived" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("alive"))
	}))
	dialer := &retiringXHTTPSystemDialer{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	internet.UseAlternativeSystemDialer(dialer)
	defer internet.UseAlternativeSystemDialer(nil)
	var once sync.Once
	unblock := func() { once.Do(func() { close(dialer.release) }) }
	defer unblock()
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	first := make(chan error, 1)
	go func() {
		stream, _, _, err := client.OpenStream(ctxA, url, "a", nil, false)
		if stream != nil {
			_ = stream.Close()
		}
		first <- err
	}()
	select {
	case <-dialer.entered:
	case <-time.After(time.Second):
		t.Fatal("first dial never entered")
	}
	cancelA()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first request did not cancel")
	}
	select {
	case <-dialer.canceled:
	case <-time.After(time.Second):
		t.Fatal("last waiter did not cancel physical dial")
	}
	secondEntered := make(chan struct{})
	var secondOnce sync.Once
	ctxB, cancelB := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelB()
	ctxB = httptrace.WithClientTrace(ctxB, &httptrace.ClientTrace{GetConn: func(string) { secondOnce.Do(func() { close(secondEntered) }) }})
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	sent := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("upload survived")); _ = writer.Close(); sent <- err }()
	second := make(chan error, 1)
	go func() {
		stream, _, _, err := client.OpenStream(ctxB, url, "b", reader, false)
		if err == nil {
			defer stream.Close()
			var data []byte
			data, err = io.ReadAll(stream)
			if err == nil && string(data) != "alive" {
				err = fmt.Errorf("unexpected response %q", data)
			}
		}
		second <- err
	}()
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second request did not encounter retiring dial")
	}
	unblock()
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("retry did not finish")
	}
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("retry closed upload body: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upload producer did not finish")
	}
}

func TestXHTTPHTTP3InternalRetryKeepsDialOwnership(t *testing.T) {
	client, url := newXHTTP3CancellationClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("alive")) }))
	transport := client.client.Transport.(*http3.Transport)
	dial := transport.Dial
	var calls atomic.Int32
	transport.Dial = func(ctx context.Context, address string, cfg *gotls.Config, qcfg *quic.Config) (*quic.Conn, error) {
		conn, err := dial(ctx, address, cfg, qcfg)
		if err == nil && calls.Add(1) == 1 {
			_ = conn.CloseWithError(0, "connection lost before first stream")
			<-conn.Context().Done()
		}
		return conn, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, _, _, err := client.OpenStream(ctx, url, "session", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "alive" {
		t.Fatalf("internal retry failed: %q %v (dials %d)", data, err, calls.Load())
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one internal redial, got %d", calls.Load())
	}
}

func TestXHTTPClientCloseOwnsUncachedQUICConnection(t *testing.T) {
	client, url := newXHTTP3CancellationClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	transport := client.client.Transport.(*http3.Transport)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cfg := transport.TLSClientConfig.Clone()
	cfg.NextProtos = []string{http3.NextProtoH3}
	conn, err := transport.Dial(ctx, url, cfg, transport.QUICConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "test cleanup")
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("client Close missed a QUIC connection absent from the HTTP transport cache")
	}
}

type retiringXHTTPSystemDialer struct {
	calls                      atomic.Int32
	entered, canceled, release chan struct{}
	base                       internet.DefaultSystemDialer
}

func TestXHTTPFailedQUICDialClosesPacketSocket(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	addr := server.LocalAddr().(*net.UDPAddr)
	dialer := &observedXHTTPPacketDialer{created: make(chan *observedXHTTPPacketConn, 1)}
	internet.UseAlternativeSystemDialer(dialer)
	defer internet.UseAlternativeSystemDialer(nil)
	client := createHTTPClient(xnet.UDPDestination(xnet.LocalHostIP, xnet.Port(addr.Port)), &internet.MemoryStreamConfig{
		ProtocolSettings: &Config{}, SecurityType: "tls", SecuritySettings: &tls.Config{NextProtocol: []string{"h3"}},
		QuicParams: &internet.QuicParams{DisableChromeParrot: true, Congestion: "reno"},
	}).(*DefaultDialerClient)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		stream, _, _, err := client.OpenStream(ctx, "https://127.0.0.1", "session", nil, false)
		if stream != nil {
			_ = stream.Close()
		}
		finished <- err
	}()
	var packet *observedXHTTPPacketConn
	select {
	case packet = <-dialer.created:
	case <-time.After(time.Second):
		t.Fatal("QUIC packet socket was not created")
	}
	defer packet.Close()
	port := packet.LocalAddr().String()
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := server.ReadFrom(make([]byte, 2048)); err != nil {
		t.Fatalf("QUIC handshake did not send a datagram: %v", err)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("canceled request did not finish")
	}
	select {
	case <-packet.closed:
	case <-time.After(time.Second):
		t.Fatal("failed QUIC handshake retained its packet socket")
	}
	rebound, err := net.ListenPacket("udp", port)
	if err != nil {
		t.Fatalf("failed QUIC dial retained local port: %v", err)
	}
	_ = rebound.Close()
}

type observedXHTTPPacketDialer struct {
	created chan *observedXHTTPPacketConn
	base    internet.DefaultSystemDialer
}

func (d *observedXHTTPPacketDialer) DestIpAddress() xnet.IP { return nil }
func (d *observedXHTTPPacketDialer) Dial(ctx context.Context, source xnet.Address, dest xnet.Destination, cfg *internet.SocketConfig) (net.Conn, error) {
	raw, err := d.base.Dial(ctx, source, dest, cfg)
	if err != nil {
		return nil, err
	}
	wrapped := raw.(*internet.PacketConnWrapper)
	packet := &observedXHTTPPacketConn{PacketConn: wrapped.PacketConn, closed: make(chan struct{})}
	wrapped.PacketConn = packet
	d.created <- packet
	return wrapped, nil
}

type observedXHTTPPacketConn struct {
	net.PacketConn
	closed chan struct{}
	once   sync.Once
}

func (c *observedXHTTPPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

func (d *retiringXHTTPSystemDialer) DestIpAddress() xnet.IP { return nil }
func (d *retiringXHTTPSystemDialer) Dial(ctx context.Context, source xnet.Address, dest xnet.Destination, cfg *internet.SocketConfig) (net.Conn, error) {
	if d.calls.Add(1) == 1 {
		close(d.entered)
		<-ctx.Done()
		close(d.canceled)
		<-d.release
		return nil, ctx.Err()
	}
	return d.base.Dial(ctx, source, dest, cfg)
}
