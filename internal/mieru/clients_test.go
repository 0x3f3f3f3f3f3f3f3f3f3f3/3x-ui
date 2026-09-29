package mieru

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/constant"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/protocol"
	"github.com/enfein/mieru/v3/pkg/stderror"
	"github.com/google/uuid"
)

func echoExisting(t *testing.T, conn net.Conn, target net.Addr) {
	t.Helper()
	payload := []byte("existing-session")
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	var got []byte
	if target.Network() == "tcp" {
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		got = make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatal(err)
		}
	} else {
		packet := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
		if _, err := packet.WriteTo(payload, target); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 128)
		n, _, err := packet.ReadFrom(buffer)
		if err != nil {
			t.Fatal(err)
		}
		got = buffer[:n]
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("existing connection returned %q, want %q", got, payload)
	}
}

func clientMux(t *testing.T, address net.Addr, user Client) *protocol.Mux {
	t.Helper()
	config, err := officialClient(t, address, user).Load()
	if err != nil {
		t.Fatal(err)
	}
	mux, err := appctlcommon.NewClientMuxFromProfile(config.Profile, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux.SetClientMultiplexFactor(3)
	t.Cleanup(func() { _ = mux.Close() })
	return mux
}

func rawNativeSession(t *testing.T, mux *protocol.Mux) net.Conn {
	t.Helper()
	conn, err := mux.DialContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func nativeRequest(t *testing.T, target net.Addr) []byte {
	t.Helper()
	var request apimodel.Request
	request.Command = constant.Socks5ConnectCmd
	if err := request.DstAddr.From(target.String()); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := request.WriteToSocks5(&buffer); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func rejectNativeRequest(t *testing.T, conn net.Conn, request []byte) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	_, writeErr := conn.Write(request)
	var response apimodel.Response
	err := response.ReadFromSocks5(conn)
	if err == nil {
		t.Fatalf("revoked credential returned SOCKS reply %d (write %v)", response.Reply, writeErr)
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, stderror.ErrTimeout) {
		t.Fatalf("unexpected native rejection: %v", err)
	}
}

func TestNativeRotationRejectsDelayedAndCachedAuthentication(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, a := mieruUser(t, db, ledger, controller, 1000)
			_, b := mieruUser(t, db, ledger, controller, 1000)
			var dials atomic.Int32
			server, err := New(Config{InboundTag: "hot-auth", Bindings: []Binding{{Network: underlay, Address: "127.0.0.1:0"}}, Clients: []Client{a, b}}, controller,
				func(ctx context.Context, d Destination) (net.Conn, error) {
					dials.Add(1)
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
			request := nativeRequest(t, target)
			mux := clientMux(t, server.Addresses()[0], a)
			pending := rawNativeSession(t, mux)
			if _, err := pending.Write(request[:1]); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				server.mu.Lock()
				authenticated := false
				for session := range server.sessions {
					authenticated = authenticated || session.UserName() != ""
				}
				server.mu.Unlock()
				if authenticated {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("partial request never reached native authentication")
				}
				time.Sleep(time.Millisecond)
			}
			var late net.Conn
			for range 16 {
				candidate := rawNativeSession(t, mux)
				if candidate.LocalAddr().String() == pending.LocalAddr().String() {
					late = candidate
					break
				}
				_ = candidate.Close()
			}
			if late == nil {
				t.Fatal("test did not obtain a session on the authenticated native underlay")
			}
			rotated := a
			rotated.Password = uuid.NewString()
			if err := server.UpdateClients([]Client{rotated, b}); err != nil {
				t.Fatal(err)
			}
			rejectNativeRequest(t, pending, request[1:])
			rejectNativeRequest(t, late, request)
			freshOld := clientMux(t, server.Addresses()[0], a)
			rejectNativeRequest(t, rawNativeSession(t, freshOld), request)
			if got := dials.Load(); got != 0 {
				t.Fatalf("retired native authentication reached target %d times", got)
			}
			for _, user := range []Client{rotated, b} {
				wireEcho(t, officialClient(t, server.Addresses()[0], user), target, []byte("valid"))
			}
			if got := dials.Load(); got != 2 {
				t.Fatalf("current identities did not reach the explicit target: %d", got)
			}
		})
	}
}

func TestNativePolicyReassignmentStartsANewAccountingIdentity(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, old := mieruUser(t, db, ledger, controller, 1500)
			_, replacement := mieruUser(t, db, ledger, controller, 500)
			replacement.Username, replacement.Password = old.Username, old.Password
			server := startNative(t, controller, underlay, old)
			target := nativeEcho(t, "udp")
			conn := wireEcho(t, officialClient(t, server.Addresses()[0], old), target, []byte("before"))
			if err := server.UpdateClients([]Client{replacement}); err != nil {
				t.Fatal(err)
			}
			requireClosedWire(t, conn)
			wireEcho(t, officialClient(t, server.Addresses()[0], replacement), target, []byte("after"))
			for _, want := range []struct {
				id          string
				raw, billed int64
			}{{old.PolicyID, 6, 18}, {replacement.PolicyID, 5, 5}} {
				account, err := ledger.Read(t.Context(), want.id)
				if err != nil || account.Up != want.raw || account.Down != want.raw || account.Billed != want.billed {
					t.Fatalf("reassigned native identity mixed billing: %+v / %v; want raw %d each, billed %d", account, err, want.raw, want.billed)
				}
			}
		})
	}
}

func TestNativeRotationReleasesIdleAuthenticatedTCPUnderlays(t *testing.T) {
	db, ledger, controller := mieruDB(t)
	_, a := mieruUser(t, db, ledger, controller, 1000)
	_, b := mieruUser(t, db, ledger, controller, 1000)
	server := startNative(t, controller, "tcp", a, b)
	target := nativeEcho(t, "tcp")
	retired := wireEcho(t, officialClient(t, server.Addresses()[0], a), target, []byte("idle"))
	unaffected := wireEcho(t, officialClient(t, server.Addresses()[0], b), target, []byte("active"))
	if err := retired.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		sessions := len(server.sessions)
		server.mu.Unlock()
		if sessions == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed logical flow still has an adapter session: %d", sessions)
		}
		time.Sleep(time.Millisecond)
	}
	server.listeners.mu.Lock()
	transports := len(server.listeners.conns)
	server.listeners.mu.Unlock()
	if transports != 2 {
		t.Fatalf("test needs one idle and one live authenticated TCP socket, got %d", transports)
	}
	a.Password = uuid.NewString()
	if err := server.UpdateClients([]Client{a, b}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		server.listeners.mu.Lock()
		transports = len(server.listeners.conns)
		server.listeners.mu.Unlock()
		if transports == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rotation retained retired user's authenticated TCP socket: %d sockets", transports)
		}
		time.Sleep(time.Millisecond)
	}
	echoExisting(t, unaffected, target)
}

func TestNativeCredentialUpdateCancelsPendingRoute(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, user := mieruUser(t, db, ledger, controller, 1000)
			entered := make(chan struct{})
			cancelled := make(chan error, 1)
			server, err := New(Config{InboundTag: "pending-route", Bindings: []Binding{{Network: "udp", Address: "127.0.0.1:0"}}, Clients: []Client{user}}, controller,
				func(ctx context.Context, d Destination) (net.Conn, error) {
					conn, err := directTestDial(ctx, d)
					if err != nil {
						cancelled <- err
						return nil, err
					}
					close(entered)
					<-ctx.Done()
					cancelled <- ctx.Err()
					_ = conn.Close()
					return nil, ctx.Err()
				})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			mux := clientMux(t, server.Addresses()[0], user)
			conn := rawNativeSession(t, mux)
			target := nativeEcho(t, network)
			request := nativeRequest(t, target)
			if network == "udp" {
				request[1] = constant.Socks5UDPAssociateCmd
			}
			if _, err := conn.Write(request); err != nil {
				t.Fatal(err)
			}
			if network == "udp" {
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				var response apimodel.Response
				if err := response.ReadFromSocks5(conn); err != nil || response.Reply != 0 {
					t.Fatalf("UDP associate handshake failed: %+v / %v", response, err)
				}
				packet := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
				if _, err := packet.WriteTo([]byte("held"), target); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("native flow did not enter the owned target connector")
			}
			user.Password = uuid.NewString()
			if err := server.UpdateClients([]Client{user}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-cancelled:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("pending route did not receive credential cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("retired credential left its target dial context active")
			}
			requireClosedWire(t, conn)
		})
	}
}

func TestNativeCredentialUpdatesRetireOnlyChangedUsers(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, a := mieruUser(t, db, ledger, controller, 1500)
			_, b := mieruUser(t, db, ledger, controller, 500)
			server := startNative(t, controller, underlay, a, b)
			aClient := officialClient(t, server.Addresses()[0], a)
			bClient := officialClient(t, server.Addresses()[0], b)
			targets := []net.Addr{nativeEcho(t, "tcp"), nativeEcho(t, "udp")}
			var aFlows, bFlows []net.Conn
			for _, target := range targets {
				aFlows = append(aFlows, wireEcho(t, aClient, target, []byte("initial")))
				bFlows = append(bFlows, wireEcho(t, bClient, target, []byte("initial")))
			}
			if err := server.UpdateClients([]Client{b, a}); err != nil {
				t.Fatal(err)
			}
			for i, target := range targets {
				echoExisting(t, aFlows[i], target)
				echoExisting(t, bFlows[i], target)
			}
			rotated := a
			rotated.Password = uuid.NewString()
			invalid := b
			invalid.Password = ""
			if err := server.UpdateClients([]Client{rotated, invalid}); !errors.Is(err, ErrConfig) {
				t.Fatalf("invalid replacement should be atomic: %v", err)
			}
			for i, target := range targets {
				echoExisting(t, aFlows[i], target)
				echoExisting(t, bFlows[i], target)
			}
			start := time.Now()
			if err := server.UpdateClients([]Client{rotated, b}); err != nil {
				t.Fatal(err)
			}
			for _, conn := range aFlows {
				requireClosedWire(t, conn)
			}
			if elapsed := time.Since(start); elapsed > 1250*time.Millisecond {
				t.Fatalf("credential cutoff exceeded bound: %v", elapsed)
			} else {
				t.Logf("rotation closed existing TCP/UDP payloads in %v", elapsed)
			}
			newClient := officialClient(t, server.Addresses()[0], rotated)
			var newFlows []net.Conn
			for i, target := range targets {
				echoExisting(t, bFlows[i], target)
				newFlows = append(newFlows, wireEcho(t, newClient, target, []byte("rotated")))
			}
			if err := server.UpdateClients([]Client{b}); err != nil {
				t.Fatal(err)
			}
			for i, conn := range newFlows {
				requireClosedWire(t, conn)
				echoExisting(t, bFlows[i], targets[i])
			}
			if err := server.UpdateClients(nil); err != nil {
				t.Fatal(err)
			}
			for _, conn := range bFlows {
				requireClosedWire(t, conn)
			}
			if err := server.UpdateClients([]Client{rotated, b}); err != nil {
				t.Fatal(err)
			}
			for _, user := range []Client{rotated, b} {
				wireEcho(t, officialClient(t, server.Addresses()[0], user), targets[1], []byte("restored"))
			}
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			if err := server.UpdateClients([]Client{a}); !errors.Is(err, ErrClosed) {
				t.Fatalf("closed server accepted credentials: %v", err)
			}
		})
	}
}
