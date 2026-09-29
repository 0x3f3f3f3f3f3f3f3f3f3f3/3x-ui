package service

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"

	dbmodel "github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func (g *policyDuplex) startMieru(t *testing.T, inbound *dbmodel.Inbound, underlay string, user dbmodel.Client, domain string) {
	t.Helper()
	address := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", inbound.Port))
	var multiplexing []appctlpb.MultiplexingLevel
	if underlay == "tcp" {
		multiplexing = append(multiplexing, appctlpb.MultiplexingLevel_MULTIPLEXING_OFF)
		t.Log("official TCP client uses the managed export's MULTIPLEXING_OFF setting")
	}
	client := productionMieruClient(t, underlay, address, user, multiplexing...)
	for range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		conn, err := client.DialContext(ctx, model.NetAddrSpec{Net: "tcp", AddrSpec: model.AddrSpec{FQDN: domain, Port: 443}})
		cancel()
		if err != nil {
			if dump, dumpErr := os.CreateTemp("/tmp", "3x-ui-mieru-dial-*.stacks"); dumpErr == nil {
				_ = pprof.Lookup("goroutine").WriteTo(dump, 2)
				_ = dump.Close()
				t.Logf("mieru dial diagnostic: inbound=%d underlay=%s client=%s channel=%d accepted=%d stacks=%s", inbound.Id, underlay, user.Email, len(g.conns), g.accepted.Load(), dump.Name())
			}
			t.Fatal(err)
		}
		g.conns = append(g.conns, conn)
		g.wg.Go(func() {
			defer g.ended.Add(1)
			buf := make([]byte, 32<<10)
			for {
				if _, err := conn.Write(buf); err != nil {
					return
				}
			}
		})
		g.wg.Go(func() {
			defer g.ended.Add(1)
			buf := make([]byte, 32<<10)
			for {
				n, err := conn.Read(buf)
				g.down.Add(int64(n))
				if err != nil {
					return
				}
			}
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for g.accepted.Load() < int64(len(g.conns)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if g.accepted.Load() != int64(len(g.conns)) {
		t.Fatal("not every official mieru channel reached the actual Xray-selected target")
	}
}

func TestClientPolicyProductionSSHAndMieruShareRates(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) { testClientPolicyProductionSharedRates(t, nil, underlay) })
	}
}

func TestClientPolicyProductionSSHAndMieruShareRates_Postgres(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			managedUsagePostgresSchema(t)
			testClientPolicyProductionSharedRates(t, nil, underlay)
		})
	}
}
