package service

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientapi "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type publicUDPRateFlow struct {
	ctx         context.Context
	up, down    atomic.Int64
	upAck       chan struct{}
	downAck     chan struct{}
	startReply  sync.Once
	payload     []byte
	conn        net.Conn
	packets     net.PacketConn
	ended       atomic.Int64
	corrupt     atomic.Bool
	destination apimodel.NetAddrSpec
}

type publicUDPRateTarget struct {
	conn    net.PacketConn
	mu      sync.Mutex
	flows   map[byte]*publicUDPRateFlow
	workers sync.WaitGroup
}

func newPublicUDPRateTarget(t *testing.T) *publicUDPRateTarget {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &publicUDPRateTarget{conn: conn, flows: make(map[byte]*publicUDPRateFlow)}
	g.workers.Go(func() {
		buffer := make([]byte, 65535)
		for {
			n, peer, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			if n == 0 {
				continue
			}
			g.mu.Lock()
			flow := g.flows[buffer[0]]
			g.mu.Unlock()
			if flow == nil || flow.ctx.Err() != nil {
				continue
			}
			if !bytes.Equal(buffer[:n], flow.payload) {
				flow.corrupt.Store(true)
				continue
			}
			flow.up.Add(int64(n))
			flow.startReply.Do(func() {
				g.workers.Go(func() { g.reply(flow, peer) })
			})
			select {
			case flow.upAck <- struct{}{}:
			case <-flow.ctx.Done():
			}
		}
	})
	t.Cleanup(func() { _ = conn.Close(); g.workers.Wait() })
	return g
}

func (g *publicUDPRateTarget) reply(flow *publicUDPRateFlow, peer net.Addr) {
	for flow.ctx.Err() == nil {
		if _, err := g.conn.WriteTo(flow.payload, peer); err != nil {
			return
		}
		select {
		case <-flow.downAck:
		case <-flow.ctx.Done():
			return
		}
	}
}

type publicUDPRateGroup struct {
	flows   []*publicUDPRateFlow
	clients []clientapi.Client
	cancel  context.CancelFunc
	workers sync.WaitGroup
	once    sync.Once
}

func (g *publicUDPRateGroup) close() {
	g.once.Do(func() {
		g.cancel()
		for _, client := range g.clients {
			_ = client.Stop()
		}
		for _, flow := range g.flows {
			_ = flow.conn.Close()
		}
		g.workers.Wait()
	})
}

func (g *publicUDPRateGroup) counts() [2]int64 {
	var counts [2]int64
	for _, flow := range g.flows {
		counts[0] += flow.up.Load()
		counts[1] += flow.down.Load()
	}
	return counts
}

func startPublicUDPRateGroup(t *testing.T, target *publicUDPRateTarget, underlay string, user model.Client, inbounds []*model.Inbound, marker byte) *publicUDPRateGroup {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	g := &publicUDPRateGroup{cancel: cancel}
	t.Cleanup(g.close)
	var multiplexing []appctlpb.MultiplexingLevel
	if underlay == "tcp" {
		multiplexing = append(multiplexing, appctlpb.MultiplexingLevel_MULTIPLEXING_OFF)
	}
	for _, inbound := range inbounds {
		address := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(inbound.Port))
		client := productionMieruClient(t, underlay, address, user, multiplexing...)
		g.clients = append(g.clients, client)
		for range 2 {
			destination := apimodel.NetAddrSpec{Net: "udp", AddrSpec: apimodel.AddrSpec{FQDN: "route.invalid", Port: 443}}
			dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
			conn, err := client.DialContext(dialCtx, destination)
			dialCancel()
			if err != nil {
				t.Fatal(err)
			}
			id := marker + byte(len(g.flows))
			flow := &publicUDPRateFlow{ctx: ctx, payload: bytes.Repeat([]byte{id}, 2048), conn: conn, upAck: make(chan struct{}, 1), downAck: make(chan struct{}, 1), destination: destination}
			flow.packets = apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
			g.flows = append(g.flows, flow)
			target.mu.Lock()
			target.flows[id] = flow
			target.mu.Unlock()
			g.workers.Go(func() {
				defer flow.ended.Add(1)
				for ctx.Err() == nil {
					if _, err := flow.packets.WriteTo(flow.payload, flow.destination); err != nil {
						return
					}
					select {
					case <-flow.upAck:
					case <-ctx.Done():
						return
					}
				}
			})
			g.workers.Go(func() {
				defer flow.ended.Add(1)
				buffer := make([]byte, 65535)
				for {
					n, _, err := flow.packets.ReadFrom(buffer)
					if err != nil {
						return
					}
					if !bytes.Equal(buffer[:n], flow.payload) {
						flow.corrupt.Store(true)
						return
					}
					flow.down.Add(int64(n))
					select {
					case flow.downAck <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
			})
		}
	}
	return g
}

func TestClientPolicyProductionMieruUDPPayloadRates(t *testing.T) {
	testClientPolicyProductionMieruUDPPayloadRates(t, false)
}

func TestClientPolicyProductionMieruUDPPayloadRates_Postgres(t *testing.T) {
	testClientPolicyProductionMieruUDPPayloadRates(t, true)
}

func testClientPolicyProductionMieruUDPPayloadRates(t *testing.T, postgres bool) {
	t.Helper()
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			var target *publicUDPRateTarget
			fixture := newProductionMieruFixtureWithTargets(t, underlay, func(t *testing.T) (net.Addr, net.Addr) {
				target = newPublicUDPRateTarget(t)
				return productionMieruEchoTarget(t, "tcp"), target.conn.LocalAddr()
			})
			clients, inbounds := &ClientService{}, &InboundService{}
			users := []model.Client{fixture.user, {Email: "packet-policy-peer", SubID: "packet-policy-peer-sub", Password: "owned-packet-policy-password", Enable: true}}
			if _, err := clients.CreateOne(inbounds, fixture.inbound.Id, users[1]); err != nil {
				t.Fatal(err)
			}
			address := netip.MustParseAddrPort(productionSSHAddress(t))
			second := &model.Inbound{Protocol: model.Mieru, Enable: true, Listen: "127.0.0.1", Port: int(address.Port()), Settings: fmt.Sprintf(`{"network":%q,"clients":[]}`, underlay)}
			if _, _, err := inbounds.AddInbound(second); err != nil {
				t.Fatal(err)
			}
			policies := make([]ClientPolicy, 2)
			for n, user := range users {
				record := lookupClientRecord(t, user.Email)
				if _, err := clients.Attach(inbounds, record.Id, []int{second.Id}); err != nil {
					t.Fatal(err)
				}
				policy, err := clients.GetPolicy(t.Context(), user.Email)
				if err != nil {
					t.Fatal(err)
				}
				policy.Multiplier = "2"
				policies[n], err = clients.UpdatePolicy(t.Context(), user.Email, policy.ClientPolicyUpdate)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := fixture.service.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			listeners := []*model.Inbound{fixture.inbound, second}
			for _, inbound := range listeners {
				waitProductionMieru(t, inbound.Id)
			}
			startGroups := func(generation byte) []*publicUDPRateGroup {
				return []*publicUDPRateGroup{
					startPublicUDPRateGroup(t, target, underlay, users[0], listeners, generation*8),
					startPublicUDPRateGroup(t, target, underlay, users[1], listeners, generation*8+4),
				}
			}
			groups := startGroups(0)
			time.Sleep(200 * time.Millisecond)
			before := [2][2]int64{groups[0].counts(), groups[1].counts()}
			start := time.Now()
			time.Sleep(400 * time.Millisecond)
			after := [2][2]int64{groups[0].counts(), groups[1].counts()}
			elapsed := time.Since(start).Seconds()
			for n := range groups {
				for direction := range 2 {
					bps := float64(after[n][direction]-before[n][direction]) / elapsed
					if bps < 8*65536 {
						t.Fatalf("public UDP unlimited client %d direction %d baseline %.0f B/s insufficient", n, direction, bps)
					}
					t.Logf("public UDP unlimited client %d direction %d: %.0f B/s", n, direction, bps)
				}
			}
			rates := [2][2]int64{{32768, 65536}, {65536, 32768}}
			apply := func(n int) {
				request := policies[n].ClientPolicyUpdate
				request.UploadBps, request.DownloadBps = rates[n][0], rates[n][1]
				var err error
				policies[n], err = clients.UpdatePolicy(t.Context(), users[n].Email, request)
				if err != nil {
					t.Fatal(err)
				}
			}
			initial := time.Now()
			apply(0)
			apply(1)
			time.Sleep(400 * time.Millisecond)
			if finished := measurePublicUDPRates(t, "initial", groups, rates); finished.Sub(initial) > 2*time.Second {
				t.Fatalf("enabling UDP limits on existing flows exceeded 2s: %s", finished.Sub(initial))
			}
			for _, next := range [][2]int64{{65536, 32768}, {32768, 65536}} {
				start := time.Now()
				rates[0] = next
				apply(0)
				time.Sleep(time.Until(start.Add(350 * time.Millisecond)))
				finished := measurePublicUDPRates(t, "live", groups, rates)
				if finished.Sub(start) > 2*time.Second {
					t.Fatalf("public UDP live edit exceeded 2s: %s", finished.Sub(start))
				}
			}
			for _, group := range groups {
				group.close()
			}
			if err := fixture.service.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			for _, inbound := range listeners {
				waitProductionMieru(t, inbound.Id)
			}
			for n, user := range users {
				policy, err := clients.GetPolicy(t.Context(), user.Email)
				if err != nil || policy.ClientPolicyUpdate != policies[n].ClientPolicyUpdate {
					t.Fatalf("restart lost UDP policy: %+v error=%v", policy, err)
				}
				up, _ := strconv.ParseInt(policy.Usage.Up, 10, 64)
				down, _ := strconv.ParseInt(policy.Usage.Down, 10, 64)
				billed, _ := strconv.ParseInt(policy.Usage.Billed, 10, 64)
				if up == 0 || down == 0 || billed != 2*(up+down) {
					t.Fatalf("UDP payload billing is not exact 2x: %+v", policy.Usage)
				}
			}
			groups = startGroups(2)
			time.Sleep(350 * time.Millisecond)
			measurePublicUDPRates(t, "restart", groups, rates)
		})
	}
}

func measurePublicUDPRates(t *testing.T, label string, groups []*publicUDPRateGroup, rates [2][2]int64) time.Time {
	t.Helper()
	before := make([][2]int64, 0, 8)
	for _, group := range groups {
		for _, flow := range group.flows {
			before = append(before, [2]int64{flow.up.Load(), flow.down.Load()})
		}
	}
	start := time.Now()
	time.Sleep(1500 * time.Millisecond)
	after := make([][2]int64, 0, 8)
	for _, group := range groups {
		for _, flow := range group.flows {
			after = append(after, [2]int64{flow.up.Load(), flow.down.Load()})
		}
	}
	finished := time.Now()
	elapsed := finished.Sub(start).Seconds()
	for n, group := range groups {
		var counts [2]int64
		for i, flow := range group.flows {
			if flow.ended.Load() != 0 || flow.corrupt.Load() {
				t.Fatalf("public UDP rate change ended/corrupted client %d flow %d", n, i)
			}
			for direction := range 2 {
				delta := after[n*4+i][direction] - before[n*4+i][direction]
				if delta <= 0 {
					t.Fatalf("%s starved client %d flow %d direction %d", label, n, i, direction)
				}
				counts[direction] += delta
			}
		}
		for direction, count := range counts {
			rate := rates[n][direction]
			// One packet of token debt plus four outstanding packets bound delivery variation.
			lower, upper := float64(rate)*elapsed*0.8, float64(rate)*elapsed*1.06+float64(rate/10)+2048+4*2048
			if float64(count) < lower || float64(count) > upper {
				t.Fatalf("%s UDP client %d direction %d: %d bytes outside [%.0f,%.0f] in %.3fs", label, n, direction, count, lower, upper, elapsed)
			}
			t.Logf("%s UDP client %d direction %d: %.0f raw B/s", label, n, direction, float64(count)/elapsed)
		}
	}
	return finished
}
