//go:build linux

package mieru

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"

	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
)

func TestOfficialTCPTransportLossReclaimsFullQueues(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "FIN"
		if reset {
			name = "RST"
		}
		for _, network := range []string{"tcp", "udp"} {
			t.Run(name+"/"+network, func(t *testing.T) {
				db, ledger, controller := mieruDB(t)
				_, user := mieruUser(t, db, ledger, controller, 2000)
				_, other := mieruUser(t, db, ledger, controller, 1000)
				server := startNative(t, controller, "tcp", user, other)
				target, received := peerCloseTarget(t, network)
				dialer := &disappearingStreamDialer{}
				client := officialClientWithDialer(t, server.Addresses()[0], user, dialer)
				conn := wireEcho(t, client, target, []byte("ready"))
				peer := wireEcho(t, officialClient(t, server.Addresses()[0], other), target, []byte("other"))
				// Keep the finite fixture's FIN inside the kernel receive window.
				// Managed queue bounds and the production listener stay unchanged.
				server.listeners.mu.Lock()
				for stream := range server.listeners.conns {
					err := stream.Conn.(*net.TCPConn).SetReadBuffer(2 << 20)
					if err != nil {
						server.listeners.mu.Unlock()
						t.Fatal(err)
					}
				}
				server.listeners.mu.Unlock()
				_ = conn.SetDeadline(time.Time{})
				if err := controller.Configure(t.Context(), user.PolicyID, policyflow.Rates{Upload: 64}); err != nil {
					t.Fatal(err)
				}
				written := make(chan error, 1)
				go func() {
					if network == "tcp" {
						_, err := conn.Write(bytes.Repeat([]byte{42}, 512<<10))
						written <- err
						return
					}
					packets := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
					for range 256 {
						if _, err := packets.WriteTo(bytes.Repeat([]byte{42}, 2048), target); err != nil {
							written <- err
							return
						}
					}
					written <- nil
				}()
				t.Cleanup(func() {
					_, _ = dialer.disconnect(true)
					_ = client.Stop()
					_ = conn.Close()
					select {
					case <-written:
					case <-time.After(3 * time.Second):
						t.Error("official client writer survived owned socket cleanup")
					}
				})
				deadline := time.Now().Add(3 * time.Second)
				for server.mux.ServerResourceStats().BufferedBytes < 240<<10 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if stats := server.mux.ServerResourceStats(); stats.BufferedBytes < 240<<10 {
					t.Fatalf("fixture did not fill both managed receive queues: %+v", stats)
				}
				select {
				case <-received:
				case <-time.After(2 * time.Second):
					t.Fatal("queued upload did not reach the shaped target")
				}
				lostAt := time.Now()
				pairs, err := dialer.disconnect(reset)
				if err != nil || len(pairs) == 0 {
					t.Fatalf("disconnect actual official TCP sockets: pairs=%v err=%v", pairs, err)
				}
				if err := client.Stop(); err != nil {
					t.Fatal(err)
				}
				deadline = lostAt.Add(2 * time.Second)
				observedFIN := false
				var states []string
				for time.Now().Before(deadline) {
					states = ownedTCPKernelStates(t, pairs)
					for _, state := range states {
						observedFIN = observedFIN || state == "08"
					}
					if stats := server.mux.ServerResourceStats(); stats.Sessions == 1 && stats.BufferedBytes == 0 && len(server.OnlineSessions()) == 1 {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				stats, online := server.mux.ServerResourceStats(), server.OnlineSessions()
				if stats.Sessions != 1 || stats.BufferedBytes != 0 || len(online) != 1 || online[0].PolicyID != other.PolicyID || time.Now().After(deadline) {
					if !reset && !observedFIN {
						t.Fatalf("physical FIN was not observed by the server kernel; fixture cannot attribute cleanup failure: states=%v native=%+v", states, stats)
					}
					t.Fatalf("physical %s retained full native queues beyond two seconds: states=%v FIN-observed=%t native=%+v online=%+v", name, states, observedFIN, stats, online)
				}
				t.Logf("physical %s reclaimed full native queues in %s", name, time.Since(lostAt))
				fresh, err := controller.Open(t.Context(), user.PolicyID)
				if err != nil {
					t.Fatal(err)
				}
				fresh.Close()
				before, err := ledger.Read(t.Context(), user.PolicyID)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("settled raw/billed after physical loss: %d/%d/%d", before.Up, before.Down, before.Billed)
				// Settled UDP packet debt still belongs to the account after its flow ends.
				// Removing the rate must not release any old canceled payload.
				if err := controller.Configure(t.Context(), user.PolicyID, policyflow.Rates{}); err != nil {
					t.Fatal(err)
				}
				echoExisting(t, peer, target)
				time.Sleep(100 * time.Millisecond)
				after, err := ledger.Read(t.Context(), user.PolicyID)
				if err != nil || before.Up != after.Up || before.Down != after.Down || before.Billed != after.Billed {
					t.Fatalf("lost TCP peer continued admitting payload: before=%+v after=%+v err=%v", before, after, err)
				}
				wireEcho(t, officialClient(t, server.Addresses()[0], user), target, []byte("again"))
			})
		}
	}
}

type ownedTCPPair struct {
	server string
	client string
}

type disappearingStreamDialer struct {
	mu           sync.Mutex
	connections  []*net.TCPConn
	disconnected bool
}

func (d *disappearingStreamDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disconnected {
		return nil, net.ErrClosed
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err == nil {
		d.connections = append(d.connections, conn.(*net.TCPConn))
	}
	return conn, err
}

func (d *disappearingStreamDialer) disconnect(reset bool) ([]ownedTCPPair, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disconnected {
		return nil, nil
	}
	d.disconnected = true
	var pairs []ownedTCPPair
	var result error
	key := func(addr net.Addr) string {
		tcp := addr.(*net.TCPAddr)
		return fmt.Sprintf("%08X:%04X", binary.NativeEndian.Uint32(tcp.IP.To4()), tcp.Port)
	}
	for _, conn := range d.connections {
		pairs = append(pairs, ownedTCPPair{key(conn.RemoteAddr()), key(conn.LocalAddr())})
		if reset {
			result = errors.Join(result, conn.SetLinger(0))
		}
		result = errors.Join(result, conn.Close())
	}
	return pairs, result
}

func ownedTCPKernelStates(t *testing.T, pairs []ownedTCPPair) []string {
	t.Helper()
	data, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		t.Fatal(err)
	}
	states := make([]string, len(pairs))
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		for i, pair := range pairs {
			if fields[1] == pair.server && fields[2] == pair.client {
				states[i] = fields[3]
			}
		}
	}
	return states
}
