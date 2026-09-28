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
