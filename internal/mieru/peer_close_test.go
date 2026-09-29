package mieru

import (
	"bytes"
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"

	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
)

func TestNativePeerCloseCancelsQueuedPolicyFlow(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		for _, network := range []string{"tcp", "udp"} {
			t.Run(underlay+"/"+network, func(t *testing.T) {
				db, ledger, controller := mieruDB(t)
				_, user := mieruUser(t, db, ledger, controller, 1000)
				_, other := mieruUser(t, db, ledger, controller, 1000)
				server := startNative(t, controller, underlay, user, other)
				target, received := peerCloseTarget(t, network)
				client := officialClient(t, server.Addresses()[0], user)
				conn := wireEcho(t, client, target, []byte("ready"))
				peer := wireEcho(t, officialClient(t, server.Addresses()[0], other), target, []byte("other"))
				t.Cleanup(func() { _ = client.Stop(); _ = conn.Close() })
				if err := conn.SetDeadline(time.Time{}); err != nil {
					t.Fatal(err)
				}
				if err := controller.Configure(t.Context(), user.PolicyID, policyflow.Rates{Upload: 64}); err != nil {
					t.Fatal(err)
				}
				written := make(chan error, 1)
				go func() {
					var err error
					if network == "tcp" {
						_, err = conn.Write(bytes.Repeat([]byte{42}, 64<<10))
					} else {
						packets := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
						for range 32 {
							if _, err = packets.WriteTo(bytes.Repeat([]byte{42}, 2048), target); err != nil {
								break
							}
						}
					}
					written <- err
				}()
				select {
				case err := <-written:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("fixture could not queue shaped payload")
				}
				deadline := time.Now().Add(time.Second)
				for server.mux.ServerResourceStats().BufferedBytes < 16<<10 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if stats := server.mux.ServerResourceStats(); stats.BufferedBytes < 16<<10 {
					t.Fatalf("fixture did not create native backpressure: %+v", stats)
				}
				select {
				case <-received:
				case <-time.After(2 * time.Second):
					t.Fatal("queued payload did not enter the shaped destination path")
				}
				closed := make(chan error, 1)
				go func() { closed <- conn.Close() }()
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("official session Close did not return")
				}
				deadline = time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					stats := server.mux.ServerResourceStats()
					if stats.Sessions == 1 && len(server.OnlineSessions()) == 1 {
						break
					}
					time.Sleep(time.Millisecond)
				}
				stats, online := server.mux.ServerResourceStats(), server.OnlineSessions()
				if stats.Sessions != 1 || stats.BufferedBytes != 0 || len(online) != 1 || online[0].PolicyID != other.PolicyID {
					t.Fatalf("peer close retained queued policy flow: native=%+v online=%+v", stats, online)
				}
				// Opening another flow synchronizes with an already-started admission transaction.
				// A closed session must leave the same client's policy usable.
				fresh, err := controller.Open(t.Context(), user.PolicyID)
				if err != nil {
					t.Fatal(err)
				}
				fresh.Close()
				before, err := ledger.Read(t.Context(), user.PolicyID)
				if err != nil {
					t.Fatal(err)
				}
				echoExisting(t, peer, target)
				time.Sleep(100 * time.Millisecond)
				after, err := ledger.Read(t.Context(), user.PolicyID)
				if err != nil || before.Up != after.Up || before.Down != after.Down || before.Billed != after.Billed {
					t.Fatalf("closed flow continued admitting payload: before=%+v after=%+v err=%v", before, after, err)
				}
			})
		}
	}
}

func TestNativePeerCloseCancelsFirstUseWait(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, user := mieruUser(t, db, ledger, controller, 1000)
			entered, canceled := make(chan struct{}), make(chan struct{})
			var dispatched atomic.Int64
			server, err := New(Config{
				InboundTag: "peer-close-first-use", Bindings: []Binding{{Network: underlay, Address: "127.0.0.1:0"}}, Clients: []Client{user},
				Authenticated: func(ctx context.Context, id string) error {
					close(entered)
					<-ctx.Done()
					close(canceled)
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
			mux := clientMux(t, server.Addresses()[0], user)
			conn := rawNativeSession(t, mux)
			if _, err := conn.Write(nativeRequest(t, nativeEcho(t, "tcp"))); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("request did not reach first-use callback")
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatalf("peer close left first-use callback alive: native=%+v", server.mux.ServerResourceStats())
			}
			if dispatched.Load() != 0 {
				t.Fatal("closed first-use session reached destination")
			}
			account, err := ledger.Read(t.Context(), user.PolicyID)
			if err != nil || account.Up != 0 || account.Down != 0 || account.Billed != 0 {
				t.Fatalf("closed first-use session consumed payload quota: %+v error=%v", account, err)
			}
		})
	}
}

func peerCloseTarget(t *testing.T, network string) (net.Addr, <-chan struct{}) {
	t.Helper()
	received := make(chan struct{})
	var once sync.Once
	if network == "udp" {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			p := make([]byte, 65535)
			for {
				n, peer, err := conn.ReadFrom(p)
				if err != nil {
					return
				}
				if n > 0 {
					if p[0] == 42 {
						once.Do(func() { close(received) })
					} else {
						_, _ = conn.WriteTo(p[:n], peer)
					}
				}
			}
		}()
		t.Cleanup(func() { _ = conn.Close(); <-done })
		return conn.LocalAddr(), received
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				p := make([]byte, 4096)
				for {
					n, err := conn.Read(p)
					if err != nil {
						return
					}
					if n > 0 {
						if p[0] == 42 {
							once.Do(func() { close(received) })
						} else {
							if _, err := conn.Write(p[:n]); err != nil {
								return
							}
						}
					}
				}
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	return listener.Addr(), received
}
