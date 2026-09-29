package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"maps"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	clientapi "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type mieruRouteObservation struct {
	source  netip.Addr
	payload [8]byte
}

func TestMieruPublicRoutesChooseObservedExit(t *testing.T) {
	testMieruPublicRoutesChooseObservedExit(t, false)
}

func TestMieruPublicRoutesChooseObservedExit_Postgres(t *testing.T) {
	testMieruPublicRoutesChooseObservedExit(t, true)
}

func testMieruPublicRoutesChooseObservedExit(t *testing.T, postgres bool) {
	t.Helper()
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			targets := make(map[string]net.Addr)
			observed := make(map[string]<-chan mieruRouteObservation)
			fixture := newProductionMieruFixtureWithTargets(t, underlay, func(t *testing.T) (net.Addr, net.Addr) {
				for _, network := range []string{"tcp", "udp"} {
					targets[network], observed[network] = mieruRouteTarget(t, network)
				}
				return targets["tcp"], targets["udp"]
			})
			peer := model.Client{Email: "route-independent", Password: "owned-route-independent-password", Enable: true}
			if _, err := (&ClientService{}).CreateOne(&InboundService{}, fixture.inbound.Id, peer); err != nil {
				t.Fatal(err)
			}
			fixture.service.ApplyPendingRestart()
			waitProductionMieru(t, fixture.inbound.Id)
			clients := []clientapi.Client{
				productionMieruClient(t, underlay, fixture.address, fixture.user, appctlpb.MultiplexingLevel_MULTIPLEXING_OFF),
				productionMieruClient(t, underlay, fixture.address, peer, appctlpb.MultiplexingLevel_MULTIPLEXING_OFF),
			}
			settings := &XraySettingService{}
			raw, err := settings.GetXrayConfigTemplate()
			if err != nil {
				t.Fatal(err)
			}
			var template map[string]any
			if err := json.Unmarshal([]byte(raw), &template); err != nil {
				t.Fatal(err)
			}
			apiRule := template["routing"].(map[string]any)["rules"].([]any)[0]
			outbounds := []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}}
			for _, network := range []string{"tcp", "udp"} {
				for _, exit := range []struct{ tag, source string }{{"a", "127.0.0.2"}, {"b", "127.0.0.3"}} {
					outbounds = append(outbounds, map[string]any{
						"tag": exit.tag + "-" + network, "protocol": "freedom", "sendThrough": exit.source,
						"settings": map[string]any{"redirect": targets[network].String()},
					})
				}
			}
			template["outbounds"] = outbounds
			var sequence uint64
			for _, network := range []string{"tcp", "udp"} {
				t.Run(network, func(t *testing.T) {
					rule := func(match map[string]any, exit string) map[string]any {
						result := maps.Clone(match)
						result["type"] = "field"
						if exit == "blocked" {
							result["outboundTag"] = exit
						} else if exit != "" {
							result["outboundTag"] = exit + "-" + network
						}
						return result
					}
					wrongNetwork := "tcp"
					if network == "tcp" {
						wrongNetwork = "udp"
					}
					userRules := []any{rule(map[string]any{"user": []string{fixture.user.Email}, "network": network}, "a"), rule(map[string]any{"user": []string{peer.Email}, "network": network}, "b")}
					for _, tc := range []struct {
						name, host string
						user       int
						rules      []any
						wants      []string
						balancers  []any
					}{
						{name: "first-user", rules: userRules, wants: []string{"127.0.0.2"}},
						{name: "second-user-same-IP", user: 1, rules: userRules, wants: []string{"127.0.0.3"}},
						{name: "domain", rules: []any{rule(map[string]any{"domain": []string{"full:route.invalid"}}, "a")}, wants: []string{"127.0.0.2"}},
						{name: "literal-IP", host: "127.0.0.7", rules: []any{rule(map[string]any{"ip": []string{"127.0.0.7/32"}}, "b")}, wants: []string{"127.0.0.3"}},
						{name: "source", rules: []any{rule(map[string]any{"source": []string{"127.0.0.1/32"}}, "a")}, wants: []string{"127.0.0.2"}},
						{name: "inbound-network-port", rules: []any{rule(map[string]any{"inboundTag": []string{fixture.inbound.Tag}, "network": network, "port": "443"}, "b")}, wants: []string{"127.0.0.3"}},
						{name: "wrong-port-blocked", rules: []any{rule(map[string]any{"port": "444"}, "a")}, wants: []string{""}},
						{name: "wrong-network-blocked", rules: []any{rule(map[string]any{"network": wrongNetwork}, "a")}, wants: []string{""}},
						{name: "first-deny-priority", rules: []any{rule(map[string]any{"user": []string{fixture.user.Email}, "network": network}, "blocked"), rule(map[string]any{"domain": []string{"full:route.invalid"}}, "a")}, wants: []string{""}},
						{
							name:      "round-robin",
							rules:     []any{rule(map[string]any{"network": network, "balancerTag": "chosen"}, "")},
							wants:     []string{"127.0.0.2", "127.0.0.3", "127.0.0.2"},
							balancers: []any{map[string]any{"tag": "chosen", "selector": []string{"a-" + network, "b-" + network}, "strategy": map[string]any{"type": "roundrobin"}}},
						},
					} {
						t.Run(tc.name, func(t *testing.T) {
							control := rule(map[string]any{"user": []string{peer.Email}, "domain": []string{"full:control.invalid"}, "network": network}, "b")
							rules := append([]any{apiRule, control}, tc.rules...)
							template["routing"] = map[string]any{"domainStrategy": "AsIs", "rules": rules, "balancers": tc.balancers}
							encoded, err := json.Marshal(template)
							if err != nil {
								t.Fatal(err)
							}
							if err := settings.SaveXraySetting(string(encoded)); err != nil {
								t.Fatal(err)
							}
							if err := fixture.service.RestartXray(false); err != nil {
								t.Fatal(err)
							}
							waitProductionMieru(t, fixture.inbound.Id)
							host := tc.host
							if host == "" {
								host = "route.invalid"
							}
							for _, want := range tc.wants {
								sequence++
								probeMieruPublicRoute(t, clients[tc.user], targets[network], observed[network], network, host, want, sequence)
								if want == "" {
									sequence++
									probeMieruPublicRoute(t, clients[1], targets[network], observed[network], network, "control.invalid", "127.0.0.3", sequence)
								}
							}
						})
					}
				})
			}
		})
	}
}

func mieruRouteTarget(t *testing.T, network string) (net.Addr, <-chan mieruRouteObservation) {
	t.Helper()
	observed := make(chan mieruRouteObservation, 128)
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
				var payload [8]byte
				copy(payload[:], buffer[:n])
				observed <- mieruRouteObservation{netip.MustParseAddrPort(peer.String()).Addr().Unmap(), payload}
				_, _ = conn.WriteTo(buffer[:n], peer)
			}
		}()
		t.Cleanup(func() { _ = conn.Close(); <-done })
		return conn.LocalAddr(), observed
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
				for {
					var payload [8]byte
					if _, err := io.ReadFull(conn, payload[:]); err != nil {
						return
					}
					observed <- mieruRouteObservation{netip.MustParseAddrPort(conn.RemoteAddr().String()).Addr().Unmap(), payload}
					if _, err := conn.Write(payload[:]); err != nil {
						return
					}
				}
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	return listener.Addr(), observed
}

func probeMieruPublicRoute(t *testing.T, client clientapi.Client, target net.Addr, observed <-chan mieruRouteObservation, network, host, want string, sequence uint64) {
	t.Helper()
	var payload [8]byte
	binary.BigEndian.PutUint64(payload[:], sequence)
	destination := apimodel.NetAddrSpec{Net: network, AddrSpec: apimodel.AddrSpec{FQDN: host, Port: 443}}
	if ip := net.ParseIP(host); ip != nil {
		destination.IP, destination.FQDN = ip, ""
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, destination)
	if err != nil && want != "" {
		t.Fatal(err)
	}
	var packets net.PacketConn
	if conn != nil {
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if network == "udp" {
			packets = apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
			_, err = packets.WriteTo(payload[:], destination)
		} else {
			_, err = conn.Write(payload[:])
		}
		if err != nil && want != "" {
			t.Fatal(err)
		}
	}
	if want == "" {
		select {
		case actual := <-observed:
			t.Fatalf("blocked route reached target: source=%s payload=%x", actual.source, actual.payload)
		case <-time.After(200 * time.Millisecond):
		}
		return
	}
	select {
	case actual := <-observed:
		if actual.source.String() != want || actual.payload != payload {
			t.Fatalf("wrong routed exit or payload: source=%s payload=%x want=%s/%x", actual.source, actual.payload, want, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("real target did not observe the probe through exit %s", want)
	}
	response := make([]byte, 8)
	if packets == nil {
		_, err = io.ReadFull(conn, response)
	} else {
		var peer net.Addr
		var n int
		n, peer, err = packets.ReadFrom(response)
		if err == nil && (n != len(response) || peer == nil || peer.String() != target.String()) {
			t.Fatalf("routed UDP reply lost its actual peer: bytes=%d peer=%v want=%v", n, peer, target)
		}
	}
	if err != nil || !bytes.Equal(response, payload[:]) {
		t.Fatalf("routed reply differs: %x err=%v", response, err)
	}
}
