package service

import (
	"bytes"
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestMieruIPv6RoutingPreservesSourceAndPolicy(t *testing.T) {
	testMieruIPv6RoutingPreservesSourceAndPolicy(t, false)
}

func TestMieruIPv6RoutingPreservesSourceAndPolicy_Postgres(t *testing.T) {
	testMieruIPv6RoutingPreservesSourceAndPolicy(t, true)
}

func testMieruIPv6RoutingPreservesSourceAndPolicy(t *testing.T, postgres bool) {
	t.Helper()
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			fixture := newProductionMieruFixtureWithTargets(t, underlay, func(t *testing.T) (net.Addr, net.Addr) {
				return productionMieruEchoTargetAt(t, "tcp", "[::1]:0"), productionMieruEchoTargetAt(t, "udp", "[::1]:0")
			})
			if err := fixture.client.Stop(); err != nil {
				t.Fatal(err)
			}
			inbounds, clients := &InboundService{}, &ClientService{}
			inbound, err := inbounds.GetInbound(fixture.inbound.Id)
			if err != nil {
				t.Fatal(err)
			}
			inbound.Listen = "::1"
			if _, _, err := inbounds.UpdateInbound(inbound); err != nil {
				t.Fatal(err)
			}
			peer := model.Client{Email: "ipv6-independent", Password: "owned-ipv6-independent-password", Enable: true}
			if _, err := clients.CreateOne(inbounds, inbound.Id, peer); err != nil {
				t.Fatal(err)
			}
			settings := &XraySettingService{}
			raw, err := settings.GetXrayConfigTemplate()
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			rules := config["routing"].(map[string]any)["rules"].([]any)
			for _, rawRule := range rules[1:] {
				rule := rawRule.(map[string]any)
				rule["inboundTag"] = []string{inbound.Tag}
				rule["user"] = []string{fixture.user.Email, peer.Email}
				rule["source"] = []string{"::1/128"}
				rule["port"] = "443"
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := settings.SaveXraySetting(string(encoded)); err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.RestartXray(false); err != nil {
				t.Fatal(err)
			}
			waitProductionMieru(t, inbound.Id)
			address := netip.AddrPortFrom(netip.IPv6Loopback(), fixture.address.Port())
			client := productionMieruClient(t, underlay, address, fixture.user)
			other := productionMieruClient(t, underlay, address, peer)
			flows, unaffected := openProductionMieruFlows(t, client), openProductionMieruFlows(t, other)
			udp := flows[1]
			payload := []byte("ipv6-peer")
			_ = udp.conn.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := udp.packets.WriteTo(payload, udp.destination); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, 64)
			n, actual, err := udp.packets.ReadFrom(response)
			if err != nil || !bytes.Equal(response[:n], payload) || actual == nil || actual.String() != fixture.udpTarget.String() {
				t.Fatalf("IPv6 routed UDP peer or payload was lost: peer=%v payload=%q err=%v", actual, response[:n], err)
			}
			users, tags, err := inbounds.GetLocalMieruOnlineUsers()
			if err != nil || len(users) != 2 || len(tags) != 1 || tags[0] != inbound.Tag {
				t.Fatalf("IPv6 public presence lost identities: users=%+v tags=%v err=%v", users, tags, err)
			}
			for _, user := range users {
				if len(user.IPs) != 1 || user.IPs[0].IP != "::1" {
					t.Fatalf("public source was replaced by the IPv4 bridge: %+v", user)
				}
			}
			ledger := database.NewClientUsageLedger(database.GetDB())
			firstID := lookupClientRecord(t, fixture.user.Email).PolicyID
			secondID := lookupClientRecord(t, peer.Email).PolicyID
			checkUsage := func(id string, raw, billed int64) {
				t.Helper()
				account, err := ledger.Read(t.Context(), id)
				if err != nil || account.Up != raw || account.Down != raw || account.Billed != billed {
					t.Fatalf("IPv6 usage duplicated or mixed: %+v err=%v; want %d each way/%d billed", account, err, raw, billed)
				}
			}
			checkUsage(firstID, 53, 159)
			checkUsage(secondID, 44, 88)
			if _, _, err := clients.SetClientEnableByEmail(inbounds, fixture.user.Email, false); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruClosed(t, flows)
			requireProductionMieruDenied(t, client)
			for _, flow := range unaffected {
				flow.echo(t)
			}
			checkUsage(firstID, 53, 159)
			checkUsage(secondID, 88, 176)
			if err := fixture.service.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			waitProductionMieru(t, inbound.Id)
			requireProductionMieruDenied(t, client)
			openProductionMieruFlows(t, productionMieruClient(t, underlay, address, peer))
			checkUsage(firstID, 53, 159)
			checkUsage(secondID, 132, 264)
		})
	}
}
