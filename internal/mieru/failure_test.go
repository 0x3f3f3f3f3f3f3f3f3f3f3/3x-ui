package mieru

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/stderror"
)

func TestNativeListenerFailureSignalsRuntimeProtection(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, user := mieruUser(t, db, ledger, controller, 1000)
			server, err := New(Config{InboundTag: "listener-failure", Bindings: []Binding{{Network: network, Address: "127.0.0.1:0"}}, Clients: []Client{user}}, controller,
				func(context.Context, Destination) (net.Conn, error) { return nil, errors.New("unexpected dispatch") })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			server.listeners.mu.Lock()
			listener := server.listeners.closers[0]
			server.listeners.mu.Unlock()
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-server.ctx.Done():
			case <-time.After(200 * time.Millisecond):
				t.Fatal("failed native listener did not signal the supervising runtime")
			}
		})
	}
}

func TestNativeShutdownNotifiesExistingTCPAndUDPFlows(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, user := mieruUser(t, db, ledger, controller, 1000)
			server := startNative(t, controller, underlay, user)
			client := officialClient(t, server.Addresses()[0], user)
			tcp := wireEcho(t, client, nativeEcho(t, "tcp"), []byte("before-shutdown"))
			udp := wireEcho(t, client, nativeEcho(t, "udp"), []byte("before-shutdown"))
			results := make(chan error, 2)
			for _, conn := range []net.Conn{tcp, udp} {
				_ = conn.SetReadDeadline(time.Now().Add(1250 * time.Millisecond))
				go func() {
					var b [1]byte
					_, err := conn.Read(b[:])
					results <- err
				}()
			}
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := <-results; !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.ErrClosedPipe) {
					t.Errorf("shutdown failed to notify an admitted flow within 1.25s: %v", err)
				}
			}
		})
	}
}

func TestNativeFirstUseFailureProtectsRoutingAndUsage(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, user := mieruUser(t, db, ledger, controller, 1000)
			var authenticated, dispatched atomic.Int64
			server, err := New(Config{
				InboundTag: "first-use-failure", Bindings: []Binding{{Network: underlay, Address: "127.0.0.1:0"}}, Clients: []Client{user},
				Authenticated: func(_ context.Context, id string) error {
					if id != user.PolicyID {
						return errors.New("wrong authenticated policy identity")
					}
					authenticated.Add(1)
					return errors.New("first-use metadata unavailable")
				},
			}, controller, func(ctx context.Context, d Destination) (net.Conn, error) {
				dispatched.Add(1)
				return directTestDial(ctx, d)
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			target := nativeEcho(t, "tcp")
			wrong := user
			wrong.Password = "invalid-first-use-password"
			mux := clientMux(t, server.Addresses()[0], wrong)
			rejectNativeRequest(t, rawNativeSession(t, mux), nativeRequest(t, target))
			if authenticated.Load() != 0 {
				t.Fatal("unauthenticated request activated first use")
			}
			client := officialClient(t, server.Addresses()[0], user)
			requireDeniedSession(t, client, target)
			if authenticated.Load() != 1 || dispatched.Load() != 0 {
				t.Fatalf("first-use failure bypassed protection: authenticated=%d dispatched=%d", authenticated.Load(), dispatched.Load())
			}
			account, err := ledger.Read(t.Context(), user.PolicyID)
			if err != nil || account.Up != 0 || account.Down != 0 || account.Billed != 0 {
				t.Fatalf("first-use failure invented payload usage: %+v err=%v", account, err)
			}
		})
	}
}

func TestNativeFirstUseWaitIsCancelledByCredentialRotation(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, user := mieruUser(t, db, ledger, controller, 1000)
			entered, cancelled := make(chan string, 1), make(chan struct{})
			var dispatched atomic.Int64
			server, err := New(Config{
				InboundTag: "pending-first-use", Bindings: []Binding{{Network: underlay, Address: "127.0.0.1:0"}}, Clients: []Client{user},
				Authenticated: func(ctx context.Context, id string) error {
					entered <- id
					<-ctx.Done()
					close(cancelled)
					return ctx.Err()
				},
			}, controller, func(ctx context.Context, d Destination) (net.Conn, error) {
				dispatched.Add(1)
				return directTestDial(ctx, d)
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			client := officialClient(t, server.Addresses()[0], user)
			target := nativeEcho(t, "tcp")
			result := make(chan error, 1)
			go func() {
				conn, err := client.DialContext(t.Context(), target)
				if conn != nil {
					_ = conn.Close()
				}
				result <- err
			}()
			select {
			case id := <-entered:
				if id != user.PolicyID {
					t.Fatalf("first-use identity=%q", id)
				}
			case <-time.After(time.Second):
				t.Fatal("authenticated request did not reach first-use callback")
			}
			rotated := user
			rotated.Password = "rotation-cancels-first-use"
			if err := server.UpdateClients([]Client{rotated}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-cancelled:
			case <-time.After(250 * time.Millisecond):
				t.Fatal("credential rotation left first-use callback blocked")
			}
			select {
			case err := <-result:
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("retired first-use request result: %v", err)
				}
			case <-time.After(1250 * time.Millisecond):
				t.Fatal("retired first-use request stayed open")
			}
			if dispatched.Load() != 0 {
				t.Fatal("retired first-use request reached routing")
			}
		})
	}
}

func TestNativeAuthenticationAndRouteFailureNeverDialDirect(t *testing.T) {
	db, ledger, controller := mieruDB(t)
	_, user := mieruUser(t, db, ledger, controller, 1000)
	var calls atomic.Int64
	server, err := New(Config{InboundTag: "denied", Bindings: []Binding{{Network: "tcp", Address: "127.0.0.1:0"}}, Clients: []Client{user}}, controller,
		func(context.Context, Destination) (net.Conn, error) {
			calls.Add(1)
			return nil, errors.New("test route explicitly denies destination")
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	target := nativeEcho(t, "tcp")
	wrong := user
	wrong.Password = "different-test-password"
	unauthorized := officialClient(t, server.Addresses()[0], wrong)
	conn, err := unauthorized.DialContext(t.Context(), target)
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, stderror.ErrTimeout) {
		t.Fatalf("wrong native credential was not rejected: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("unauthenticated identity reached dispatch %d times", got)
	}
	valid := officialClient(t, server.Addresses()[0], user)
	requireDeniedSession(t, valid, target)
	if got := calls.Load(); got != 1 {
		t.Fatalf("authenticated route denial did not run exactly once: %d", got)
	}
	account, err := ledger.Read(t.Context(), user.PolicyID)
	if err != nil || account.Up != 0 || account.Down != 0 || account.Billed != 0 {
		t.Fatalf("failed authentication/route invented payload charges: %+v / %v", account, err)
	}
}

func TestNativePartialStartupReleasesOwnedListeners(t *testing.T) {
	db, ledger, controller := mieruDB(t)
	_, user := mieruUser(t, db, ledger, controller, 1000)
	occupied, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	config := Config{InboundTag: "collision", Bindings: []Binding{{Network: "tcp", Address: "127.0.0.1:0"}, {Network: "udp", Address: occupied.LocalAddr().String()}}, Clients: []Client{user}}
	if _, err := New(config, controller, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing routing callback was accepted: %v", err)
	}
	server, err := New(config, controller, directTestDial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if err := server.Start(); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("occupied transport binding did not fail startup: %v", err)
	}
	addresses := server.Addresses()
	if len(addresses) != 1 || addresses[0].Network() != "tcp" {
		t.Fatalf("test did not exercise partial listener acquisition: %v", addresses)
	}
	rebound, err := net.Listen("tcp", addresses[0].String())
	if err != nil {
		t.Fatalf("failed startup leaked its other listener: %v", err)
	}
	_ = rebound.Close()
	if err := server.Start(); !errors.Is(err, ErrClosed) {
		t.Fatalf("failed instance silently restarted: %v", err)
	}
}

func TestNativePartialHandshakeCannotClearItsOverallDeadline(t *testing.T) {
	db, ledger, controller := mieruDB(t)
	_, user := mieruUser(t, db, ledger, controller, 1000)
	server := startNative(t, controller, "tcp", user)
	api := officialClient(t, server.Addresses()[0], user)
	config, err := api.Load()
	if err != nil {
		t.Fatal(err)
	}
	mux, err := appctlcommon.NewClientMuxFromProfile(config.Profile, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	conn, err := mux.DialContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{5}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(7 * time.Second))
	var p [1]byte
	if n, err := conn.Read(p[:]); n != 0 || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe)) {
		t.Fatalf("partial authenticated handshake occupied its slot past the deadline: %d / %v", n, err)
	}
	account, err := ledger.Read(t.Context(), user.PolicyID)
	if err != nil || account.Up != 0 || account.Down != 0 || account.Billed != 0 {
		t.Fatalf("partial handshake was charged as payload: %+v / %v", account, err)
	}
}
