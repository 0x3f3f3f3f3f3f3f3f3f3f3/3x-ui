package mieru

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientapi "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type nativeDuplexGroup struct {
	up, down [4]atomic.Int64
	conns    []net.Conn
	clients  []clientapi.Client
	targets  []func()
	workers  sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
}

func (g *nativeDuplexGroup) close() {
	g.cancel()
	for _, client := range g.clients {
		_ = client.Stop()
	}
	for _, conn := range g.conns {
		_ = conn.Close()
	}
	for _, closeTarget := range g.targets {
		closeTarget()
	}
	g.workers.Wait()
}

func (g *nativeDuplexGroup) counts() [2]int64 {
	var counts [2]int64
	for n := range 4 {
		counts[0] += g.up[n].Load()
		counts[1] += g.down[n].Load()
	}
	return counts
}

func nativeDuplexTarget(t *testing.T, network string, up *atomic.Int64, uploadRead, downloadRead chan struct{}) (net.Addr, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	if network == "udp" {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		workers.Go(func() {
			var once sync.Once
			p := make([]byte, 4096)
			for {
				n, peer, err := conn.ReadFrom(p)
				if err != nil {
					return
				}
				up.Add(int64(n))
				once.Do(func() {
					workers.Go(func() {
						ticker := time.NewTicker(time.Millisecond)
						defer ticker.Stop()
						p := make([]byte, 4096)
						for {
							if _, err := conn.WriteTo(p, peer); err != nil {
								return
							}
							if downloadRead == nil {
								select {
								case <-ticker.C:
								case <-ctx.Done():
									return
								}
								continue
							}
							select {
							case <-ctx.Done():
								return
							case <-downloadRead:
							}
						}
					})
				})
				if uploadRead == nil {
					continue
				}
				select {
				case uploadRead <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		})
		closeTarget := func() { cancel(); _ = conn.Close(); workers.Wait() }
		t.Cleanup(closeTarget)
		return conn.LocalAddr(), closeTarget
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	workers.Go(func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		workers.Go(func() {
			p := make([]byte, 4096)
			for {
				if _, err := conn.Write(p); err != nil {
					return
				}
				if downloadRead == nil {
					continue
				}
				select {
				case <-downloadRead:
				case <-ctx.Done():
					return
				}
			}
		})
		p := make([]byte, 4096)
		for {
			n, err := io.ReadFull(conn, p)
			up.Add(int64(n))
			if err != nil {
				return
			}
			if uploadRead == nil {
				continue
			}
			select {
			case uploadRead <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	})
	closeTarget := func() { cancel(); _ = listener.Close(); workers.Wait() }
	t.Cleanup(closeTarget)
	return listener.Addr(), closeTarget
}

type connectedPacket struct {
	net.PacketConn
	destination net.Addr
}

func (p connectedPacket) Write(b []byte) (int, error) { return p.WriteTo(b, p.destination) }
func (p connectedPacket) Read(b []byte) (int, error) {
	n, _, err := p.ReadFrom(b)
	return n, err
}

func nativeDuplex(t *testing.T, servers []*Server, user Client, bounded bool) *nativeDuplexGroup {
	t.Helper()
	g := &nativeDuplexGroup{}
	g.ctx, g.cancel = context.WithCancel(t.Context())
	for n := range 4 {
		network := "tcp"
		if n%2 == 1 {
			network = "udp"
		}
		client := officialClient(t, servers[n/2].Addresses()[0], user)
		g.clients = append(g.clients, client)
		var uploadRead, downloadRead chan struct{}
		if bounded {
			uploadRead, downloadRead = make(chan struct{}), make(chan struct{})
		}
		target, closeTarget := nativeDuplexTarget(t, network, &g.up[n], uploadRead, downloadRead)
		g.targets = append(g.targets, closeTarget)
		conn, err := client.DialContext(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		g.conns = append(g.conns, conn)
		var stream io.ReadWriter = conn
		if network == "udp" {
			stream = connectedPacket{PacketConn: apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn)), destination: target}
		}
		g.workers.Go(func() {
			p := make([]byte, 4096)
			for {
				if _, err := stream.Write(p); err != nil {
					return
				}
				if !bounded {
					continue
				}
				select {
				case <-uploadRead:
				case <-g.ctx.Done():
					return
				}
			}
		})
		g.workers.Go(func() {
			p := make([]byte, 4096)
			for {
				count, err := io.ReadFull(stream, p)
				g.down[n].Add(int64(count))
				if err != nil {
					return
				}
				if !bounded {
					continue
				}
				select {
				case downloadRead <- struct{}{}:
				case <-g.ctx.Done():
					return
				}
			}
		})
	}
	t.Cleanup(g.close)
	return g
}

func TestNativeMixedStreamsAndPacketsShareDuplexRatesAcrossListeners(t *testing.T) {
	db, ledger, controller := mieruDB(t)
	a, userA := mieruUser(t, db, ledger, controller, 2000)
	b, userB := mieruUser(t, db, ledger, controller, 500)
	baselineRecord, baselineUser := mieruUser(t, db, ledger, controller, 1000)
	for _, record := range []model.ClientRecord{a, b, baselineRecord} {
		if err := db.Model(&record).Update("total_gb", 0).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Update("total", 0).Error; err != nil {
			t.Fatal(err)
		}
	}
	servers := []*Server{startNative(t, controller, "tcp", userA, userB, baselineUser), startNative(t, controller, "udp", userA, userB, baselineUser)}
	baseline := nativeDuplex(t, servers, baselineUser, false)
	time.Sleep(200 * time.Millisecond)
	before, start := baseline.counts(), time.Now()
	time.Sleep(400 * time.Millisecond)
	elapsed, after := time.Since(start).Seconds(), baseline.counts()
	for direction := range 2 {
		bps := float64(after[direction]-before[direction]) / elapsed
		if bps < 8*65536 {
			t.Fatalf("native unlimited direction %d baseline %.0f B/s is insufficient", direction, bps)
		}
		t.Logf("native unlimited direction %d: %.0f B/s", direction, bps)
	}
	baseline.close()
	rates := []policyflow.Rates{{Upload: 32768, Download: 65536}, {Upload: 65536, Download: 32768}}
	for i, id := range []string{a.PolicyID, b.PolicyID} {
		if err := controller.Configure(t.Context(), id, rates[i]); err != nil {
			t.Fatal(err)
		}
	}
	groups := []*nativeDuplexGroup{nativeDuplex(t, servers, userA, true), nativeDuplex(t, servers, userB, true)}
	measure := func(label string, duration time.Duration) {
		t.Helper()
		before := [2][2]int64{groups[0].counts(), groups[1].counts()}
		start := time.Now()
		time.Sleep(duration)
		elapsed := time.Since(start).Seconds()
		for i, group := range groups {
			after := group.counts()
			for direction, rate := range []int64{rates[i].Upload, rates[i].Download} {
				count := after[direction] - before[i][direction]
				lower := float64(rate) * elapsed * 0.80
				// Four paths each hold one 4096-byte payload awaiting independent receipt.
				upper := float64(rate)*elapsed*1.06 + float64(rate/10) + 4096 + 4*4096
				if float64(count) < lower || float64(count) > upper {
					for n := range 4 {
						t.Logf("transport %d total upload/download: %d/%d", n, group.up[n].Load(), group.down[n].Load())
					}
					t.Fatalf("%s client %d direction %d: %d bytes outside [%.0f, %.0f] in %.3fs", label, i, direction, count, lower, upper, elapsed)
				}
				t.Logf("%s client %d direction %d: %.0f B/s", label, i, direction, float64(count)/elapsed)
			}
		}
	}
	time.Sleep(300 * time.Millisecond)
	measure("initial", 1800*time.Millisecond)
	for _, next := range []policyflow.Rates{{Upload: 65536, Download: 32768}, {Upload: 32768, Download: 65536}} {
		start := time.Now()
		rates[0] = next
		if err := controller.Configure(t.Context(), a.PolicyID, next); err != nil {
			t.Fatal(err)
		}
		time.Sleep(250 * time.Millisecond)
		measure(fmt.Sprintf("live %d/%d", next.Upload, next.Download), 1500*time.Millisecond)
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("live rate acceptance exceeded 2 seconds: %v", elapsed)
		}
	}
	for _, group := range groups {
		for n := range 4 {
			if group.up[n].Load() < 4096 || group.down[n].Load() < 4096 {
				t.Fatalf("native transport combination %d did not remain usable", n)
			}
		}
		group.close()
	}
	account, err := ledger.Read(t.Context(), a.PolicyID)
	if err != nil || account.Up == 0 || account.Down == 0 || account.Billed != 2*(account.Up+account.Down) {
		t.Fatalf("raw rate shaping confused 2x billing: %+v / %v", account, err)
	}
}
