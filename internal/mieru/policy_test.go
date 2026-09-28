package mieru

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientapi "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/stderror"

	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func directTestDial(ctx context.Context, d Destination) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, d.Network, net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port))))
}

func startNative(t *testing.T, controller *policyflow.Controller, underlay string, clients ...Client) *Server {
	t.Helper()
	s, err := New(Config{InboundTag: "mieru-policy", Bindings: []Binding{{Network: underlay, Address: "127.0.0.1:0"}}, Clients: clients}, controller, directTestDial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	return s
}

func requireClosedWire(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(1250 * time.Millisecond))
	var p [1]byte
	n, err := conn.Read(p[:])
	if n != 0 || err == nil {
		t.Fatalf("revoked native session still returned payload: %d / %v", n, err)
	}
	var timeout net.Error
	if errors.Is(err, stderror.ErrTimeout) || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("native session stayed open past policy cutoff: %v", err)
	}
}

func requireDeniedSession(t *testing.T, client clientapi.Client, target net.Addr) {
	t.Helper()
	conn, err := client.DialContext(t.Context(), target)
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("exhausted/disabled client opened another native session: %v", err)
	}
}

func TestNativeUDPQuotaDropsWholeReplyAndClosesExistingTCPAndUDP(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			record, user := mieruUser(t, db, ledger, controller, 1500)
			_, other := mieruUser(t, db, ledger, controller, 1000)
			if err := db.Model(&record).Update("total_gb", 15).Error; err != nil {
				t.Fatal(err)
			}
			server := startNative(t, controller, underlay, user, other)
			client := officialClient(t, server.Addresses()[0], user)
			tcpTarget, udpTarget := nativeEcho(t, "tcp"), nativeEcho(t, "udp")
			idleTCP, err := client.DialContext(t.Context(), tcpTarget)
			if err != nil {
				t.Fatal(err)
			}
			defer idleTCP.Close()
			conn, err := client.DialContext(t.Context(), udpTarget)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			packet := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
			if _, err := packet.WriteTo([]byte("123456"), udpTarget); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			p := make([]byte, 65535)
			if n, _, err := packet.ReadFrom(p); n != 0 || err == nil {
				t.Fatalf("oversized quota reply was partially delivered: %d / %v", n, err)
			} else {
				var timeout net.Error
				if !errors.Is(err, stderror.ErrTimeout) && (!errors.As(err, &timeout) || !timeout.Timeout()) {
					t.Fatalf("association closed before remaining quota was usable: %v", err)
				}
			}
			account, err := ledger.Read(t.Context(), record.PolicyID)
			if err != nil || account.Up != 6 || account.Down != 0 || account.Billed != 9 {
				t.Fatalf("whole reply rejection changed usage: %+v / %v", account, err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := packet.WriteTo([]byte("ok"), udpTarget); err != nil {
				t.Fatal(err)
			}
			if n, _, err := packet.ReadFrom(p); err != nil || !bytes.Equal(p[:n], []byte("ok")) {
				t.Fatalf("smaller packet could not use remainder: %d / %v", n, err)
			}
			start := time.Now()
			requireClosedWire(t, idleTCP)
			requireClosedWire(t, conn)
			if elapsed := time.Since(start); elapsed > 1250*time.Millisecond {
				t.Fatalf("native aggregate cutoff exceeded bound: %v", elapsed)
			} else {
				t.Logf("quota retired existing TCP and UDP in %v", elapsed)
			}
			account, err = ledger.Read(t.Context(), record.PolicyID)
			if err != nil || account.Up != 8 || account.Down != 2 || account.Billed != 15 {
				t.Fatalf("quota accounting = %+v / %v", account, err)
			}
			requireDeniedSession(t, client, tcpTarget)
			requireDeniedSession(t, client, udpTarget)
			independent := officialClient(t, server.Addresses()[0], other)
			wireEcho(t, independent, udpTarget, []byte("unaffected"))
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			restarted := startNative(t, controller, underlay, user, other)
			requireDeniedSession(t, officialClient(t, restarted.Addresses()[0], user), udpTarget)
		})
	}
}

func TestNativeDisableAndMalformedUDPReleaseOnlyTheirSessions(t *testing.T) {
	db, ledger, controller := mieruDB(t)
	record, user := mieruUser(t, db, ledger, controller, 1000)
	_, other := mieruUser(t, db, ledger, controller, 1000)
	server := startNative(t, controller, "udp", user, other)
	client := officialClient(t, server.Addresses()[0], user)
	target := nativeEcho(t, "udp")
	conn := wireEcho(t, client, target, []byte("active"))
	broken := wireEcho(t, client, target, []byte("second"))
	tunnel := apicommon.NewPacketOverStreamTunnel(broken)
	var address apimodel.AddrSpec
	if err := address.From(target.String()); err != nil {
		t.Fatal(err)
	}
	frame := bytes.NewBuffer([]byte{0, 0, 1})
	if err := address.WriteToSocks5(frame); err != nil {
		t.Fatal(err)
	}
	frame.WriteString("must not reach target")
	if _, err := tunnel.Write(frame.Bytes()); err != nil {
		t.Fatal(err)
	}
	requireClosedWire(t, broken)
	account, err := ledger.Read(t.Context(), record.PolicyID)
	if err != nil || account.Up != 12 || account.Down != 12 {
		t.Fatalf("malformed packet was admitted: %+v / %v", account, err)
	}
	if err := db.Model(&record).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	requireClosedWire(t, conn)
	requireDeniedSession(t, client, target)
	independent := officialClient(t, server.Addresses()[0], other)
	live := wireEcho(t, independent, target, []byte("still active"))
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	requireClosedWire(t, live)
}

type pausedReadConn struct {
	net.Conn
	paused atomic.Bool
	closed chan struct{}
	once   sync.Once
}

func (c *pausedReadConn) Read(p []byte) (int, error) {
	if c.paused.Load() {
		<-c.closed
		return 0, net.ErrClosed
	}
	return c.Conn.Read(p)
}

func (c *pausedReadConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *pausedReadConn) SetDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		return c.Close()
	}
	return c.Conn.SetDeadline(deadline)
}

type stalledReadDialer struct{ conn *pausedReadConn }

func (d *stalledReadDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	_ = conn.(*net.TCPConn).SetReadBuffer(4096)
	d.conn = &pausedReadConn{Conn: conn, closed: make(chan struct{})}
	return d.conn, nil
}

func TestNativeShutdownReleasesBackpressuredAuthenticatedSessions(t *testing.T) {
	for _, action := range []string{"shutdown", "disable"} {
		t.Run(action, func(t *testing.T) { nativeBackpressure(t, action == "disable") })
	}
}

func TestNativeOneWayUDPKeepsItsActiveTargetMapping(t *testing.T) {
	db, ledger, controller := mieruDB(t)
	_, user := mieruUser(t, db, ledger, controller, 1000)
	server := startNative(t, controller, "udp", user)
	client := officialClient(t, server.Addresses()[0], user)
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	conn, err := client.DialContext(t.Context(), target.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	packet := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(31 * time.Second)
	var firstSource string
	var sent int64
	for time.Now().Before(deadline) {
		<-ticker.C
		if _, err := packet.WriteTo([]byte("live"), target.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		_ = target.SetReadDeadline(time.Now().Add(time.Second))
		var p [8]byte
		n, source, err := target.ReadFrom(p[:])
		if err != nil || !bytes.Equal(p[:n], []byte("live")) {
			t.Fatalf("active upload-only UDP target stopped: %d / %v", n, err)
		}
		if firstSource == "" {
			firstSource = source.String()
		} else if firstSource != source.String() {
			t.Fatalf("active target mapping expired despite continuous uploads: %s -> %s", firstSource, source)
		}
		sent += 4
	}
	account, err := ledger.Read(t.Context(), user.PolicyID)
	if err != nil || account.Up != sent || account.Down != 0 || account.Billed != sent {
		t.Fatalf("one-way UDP usage = %+v / %v; want %d upload only", account, err, sent)
	}
}

func nativeBackpressure(t *testing.T, disable bool) {
	t.Helper()
	db, ledger, controller := mieruDB(t)
	record, user := mieruUser(t, db, ledger, controller, 1000)
	_, other := mieruUser(t, db, ledger, controller, 1000)
	if err := db.Model(&record).Update("total_gb", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Update("total", 0).Error; err != nil {
		t.Fatal(err)
	}
	server := startNative(t, controller, "tcp", user, other)
	dialer := &stalledReadDialer{}
	client := officialClientWithDialer(t, server.Addresses()[0], user, dialer)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	targetDone := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	workers.Go(func() {
		defer close(targetDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		p := make([]byte, 65536)
		for {
			if _, err := conn.Write(p); err != nil {
				return
			}
		}
	})
	conn, err := client.DialContext(t.Context(), listener.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(); _ = conn.Close() }()
	dialer.conn.paused.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	var previous int64
	for {
		time.Sleep(150 * time.Millisecond)
		account, err := ledger.Read(t.Context(), record.PolicyID)
		if err != nil {
			t.Fatal(err)
		}
		if account.Down > 65536 && account.Down == previous {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("test did not establish native receive backpressure: previous=%d, down=%d", previous, account.Down)
		}
		previous = account.Down
	}
	start := time.Now()
	if disable {
		if err := db.Model(&record).Update("enable", false).Error; err != nil {
			t.Fatal(err)
		}
		select {
		case <-targetDone:
			t.Logf("backpressured disabled target closed in %v", time.Since(start))
		case <-time.After(1250 * time.Millisecond):
			_ = client.Stop()
			t.Fatal("disabled client's blocked native Close prevented its target from closing")
		}
		independent := officialClient(t, server.Addresses()[0], other)
		wireEcho(t, independent, nativeEcho(t, "tcp"), []byte("unaffected"))
		return
	}
	done := make(chan error, 1)
	go func() { done <- server.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("backpressured server closed in %v", time.Since(start))
	case <-time.After(2 * time.Second):
		// Stop the owned client to release the native send lock before reporting failure.
		_ = client.Stop()
		<-done
		t.Fatal("server shutdown blocked on the native session send lock")
	}
}
