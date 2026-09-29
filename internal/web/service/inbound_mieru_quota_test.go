package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/enfein/mieru/v3/pkg/stderror"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestMieruInboundNaturalQuota(t *testing.T) {
	testMieruInboundNaturalQuota(t, false)
}

func TestMieruInboundNaturalQuota_Postgres(t *testing.T) {
	testMieruInboundNaturalQuota(t, true)
}

func testMieruInboundNaturalQuota(t *testing.T, postgres bool) {
	t.Helper()
	for _, underlay := range []string{"tcp", "udp"} {
		for _, tc := range []struct {
			multiplier string
			quota      int64
		}{
			{"0.5", 4 << 20}, {"1", 8 << 20}, {"1.5", 12 << 20}, {"2", 16 << 20},
		} {
			t.Run(underlay+"/"+tc.multiplier, func(t *testing.T) {
				if postgres {
					managedUsagePostgresSchema(t)
				}
				var targetUp atomic.Int64
				fixture := newProductionMieruFixtureWithTargets(t, underlay, func(t *testing.T) (net.Addr, net.Addr) {
					return productionMieruCountedEcho(t, "tcp", &targetUp), productionMieruCountedEcho(t, "udp", &targetUp)
				})
				clients, inbounds := &ClientService{}, &InboundService{}
				peer := model.Client{Email: "quota-independent", Password: "owned-independent-quota-password", Enable: true}
				if _, err := clients.CreateOne(inbounds, fixture.inbound.Id, peer); err != nil {
					t.Fatal(err)
				}
				if err := fixture.service.RestartXray(true); err != nil {
					t.Fatal(err)
				}
				waitProductionMieru(t, fixture.inbound.Id)
				policy, err := clients.GetPolicy(t.Context(), fixture.user.Email)
				if err != nil {
					t.Fatal(err)
				}
				policy.Multiplier = tc.multiplier
				if _, err := clients.UpdatePolicy(t.Context(), fixture.user.Email, policy.ClientPolicyUpdate); err != nil {
					t.Fatal(err)
				}
				var multiplexing []appctlpb.MultiplexingLevel
				if underlay == "tcp" {
					multiplexing = append(multiplexing, appctlpb.MultiplexingLevel_MULTIPLEXING_OFF)
				}
				client := productionMieruClient(t, underlay, fixture.address, fixture.user, multiplexing...)
				other := productionMieruClient(t, underlay, fixture.address, peer, multiplexing...)
				unaffected := openProductionMieruFlows(t, other)
				flows := append(openProductionMieruFlows(t, client), openProductionMieruFlows(t, client)...)
				record := lookupClientRecord(t, fixture.user.Email)
				ledger := database.NewClientUsageLedger(database.GetDB())
				before, err := ledger.Read(t.Context(), record.PolicyID)
				if err != nil {
					t.Fatal(err)
				}
				updated := *record.ToClient()
				updated.TotalGB = before.Billed + tc.quota
				if _, err := clients.Update(inbounds, record.Id, updated, 0); err != nil {
					t.Fatal(err)
				}
				upStart := targetUp.Load()
				observedDown, cutoff := runProductionMieruQuota(t, flows, ledger, record.PolicyID, updated.TotalGB)
				after, err := ledger.Read(t.Context(), record.PolicyID)
				if err != nil {
					t.Fatal(err)
				}
				up, down := after.Up-before.Up, after.Down-before.Down
				observedUp := targetUp.Load() - upStart
				// Four acknowledged exchanges bound all undelivered bytes, including every hidden buffer.
				const flightBound = 2 * (2*16384 + 2*8192)
				if up+down != 8<<20 || after.Billed != updated.TotalGB || after.Remainder != 0 {
					t.Fatalf("natural quota boundary: up=%d down=%d billed=%d want=%d remainder=%d", up, down, after.Billed, updated.TotalGB, after.Remainder)
				}
				if observedUp > up || observedDown > down || up+down-observedUp-observedDown > flightBound {
					t.Fatalf("independent payload/accounting mismatch: admitted=%d/%d observed=%d/%d flight bound=%d", up, down, observedUp, observedDown, flightBound)
				}
				t.Logf("native=%s multiplier=%s raw=%d/%d observed=%d/%d billed delta=%d quota overshoot=0 flight=%d/%d cutoff upper=%s", underlay, tc.multiplier, up, down, observedUp, observedDown, after.Billed-before.Billed, up+down-observedUp-observedDown, flightBound, cutoff)
				for _, flow := range unaffected {
					flow.echo(t)
				}
				requireProductionMieruDenied(t, client)
				if err := fixture.service.RestartXray(true); err != nil {
					t.Fatal(err)
				}
				waitProductionMieru(t, fixture.inbound.Id)
				restarted := productionMieruClient(t, underlay, fixture.address, updated, multiplexing...)
				requireProductionMieruDenied(t, restarted)
				durable, err := ledger.Read(t.Context(), record.PolicyID)
				if err != nil || durable.Up != after.Up || durable.Down != after.Down || durable.Billed != after.Billed || durable.Remainder != after.Remainder {
					t.Fatalf("exhausted restart changed durable accounting: before=%+v after=%+v error=%v", after, durable, err)
				}
				for _, flow := range openProductionMieruFlows(t, productionMieruClient(t, underlay, fixture.address, peer, multiplexing...)) {
					flow.echo(t)
				}
			})
		}
	}
}

type productionMieruQuotaResult struct {
	down    int64
	network string
	ended   time.Time
	err     error
}

func runProductionMieruQuota(t *testing.T, flows []productionMieruFlow, ledger *database.ClientUsageLedger, policyID string, quota int64) (int64, time.Duration) {
	t.Helper()
	results := make(chan productionMieruQuotaResult, len(flows))
	var workers sync.WaitGroup
	start := make(chan struct{})
	for _, flow := range flows {
		workers.Go(func() {
			<-start
			results <- exchangeProductionMieruQuota(flow)
		})
	}
	t.Cleanup(func() {
		for _, flow := range flows {
			_ = flow.conn.Close()
		}
		workers.Wait()
	})
	lastBelow, exhausted := time.Now(), false
	close(start)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(45 * time.Second)
	defer timeout.Stop()
	var down int64
	var ended time.Time
	perNetwork := make(map[string]int64)
	for pending := len(flows); pending > 0; {
		select {
		case result := <-results:
			var timeout net.Error
			if result.err == nil || errors.Is(result.err, stderror.ErrTimeout) || (errors.As(result.err, &timeout) && timeout.Timeout()) {
				t.Fatalf("natural quota left %s flow stalled instead of closing: %v", result.network, result.err)
			}
			if !errors.Is(result.err, io.EOF) && !errors.Is(result.err, io.ErrUnexpectedEOF) && !errors.Is(result.err, io.ErrClosedPipe) && !errors.Is(result.err, net.ErrClosed) {
				t.Fatalf("unexpected natural-quota %s failure: %v", result.network, result.err)
			}
			down += result.down
			perNetwork[result.network] += result.down
			if result.ended.After(ended) {
				ended = result.ended
			}
			pending--
		case <-ticker.C:
			readAt := time.Now()
			usage, err := ledger.Read(t.Context(), policyID)
			if err != nil {
				t.Fatal(err)
			}
			if usage.Billed < quota {
				lastBelow = readAt
			} else {
				exhausted = true
			}
			if exhausted && time.Since(lastBelow) > 1250*time.Millisecond {
				t.Fatal("natural quota did not close all existing flows within 1250ms")
			}
		case <-timeout.C:
			t.Fatal("natural quota did not exhaust within 45s")
		}
	}
	if ended.Sub(lastBelow) > 1250*time.Millisecond {
		t.Fatalf("natural-quota cutoff upper bound=%s exceeds 1250ms", ended.Sub(lastBelow))
	}
	for _, network := range []string{"tcp", "udp"} {
		if perNetwork[network] < 64<<10 {
			t.Fatalf("%s payload did not participate in quota competition: %d bytes", network, perNetwork[network])
		}
	}
	return down, ended.Sub(lastBelow)
}

func exchangeProductionMieruQuota(flow productionMieruFlow) productionMieruQuotaResult {
	size := 16 << 10
	if flow.packets != nil {
		size = 8 << 10
	}
	payload, response := bytes.Repeat([]byte{0x5a}, size), make([]byte, size)
	result := productionMieruQuotaResult{network: flow.destination.Net}
	for result.err == nil {
		_ = flow.conn.SetDeadline(time.Now().Add(2 * time.Second))
		var n int
		if flow.packets == nil {
			_, result.err = flow.conn.Write(payload)
			if result.err == nil {
				n, result.err = io.ReadFull(flow.conn, response)
			}
		} else {
			_, result.err = flow.packets.WriteTo(payload, flow.destination)
			if result.err == nil {
				n, _, result.err = flow.packets.ReadFrom(response)
				if result.err == nil && n != len(payload) {
					result.err = fmt.Errorf("UDP payload truncated: %d", n)
				}
			}
		}
		result.down += int64(n)
		if !bytes.Equal(response[:n], payload[:n]) {
			result.err = errors.New("quota echo payload corrupted")
		}
	}
	result.ended = time.Now()
	return result
}

func productionMieruCountedEcho(t *testing.T, network string, up *atomic.Int64) net.Addr {
	t.Helper()
	if network == "udp" {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			buffer := make([]byte, 65535)
			for {
				n, peer, err := conn.ReadFrom(buffer)
				if err != nil {
					return
				}
				up.Add(int64(n))
				_, _ = conn.WriteTo(buffer[:n], peer)
			}
		}()
		t.Cleanup(func() { _ = conn.Close(); <-done })
		return conn.LocalAddr()
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
				buffer := make([]byte, 16<<10)
				for {
					n, readErr := conn.Read(buffer)
					up.Add(int64(n))
					if n > 0 {
						if _, err := conn.Write(buffer[:n]); err != nil {
							return
						}
					}
					if readErr != nil {
						return
					}
				}
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	return listener.Addr()
}
