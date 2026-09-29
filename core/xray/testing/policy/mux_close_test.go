package policy_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	appstats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/stats"
)

func TestLegacyMuxCloseReleasesMeteredTCPAndXUDP(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			target := tcpEcho(t)
			targetPort := target.Addr().(*net.TCPAddr).Port
			if network == "udp" {
				packet, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", targetPort))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = packet.Close() })
				go func() {
					buffer := make([]byte, 1024)
					for {
						n, addr, err := packet.ReadFrom(buffer)
						if err != nil {
							return
						}
						_, _ = packet.WriteTo(buffer[:n], addr)
					}
				}()
			}
			serverPort := port(t)
			start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"vmess","settings":{"clients":[{"id":"2b831278-cc9f-42a8-a52a-28ac96ea41c3"}]}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, serverPort))
			listen := port(t)
			client := start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"stats":{},"policy":{"system":{"statsOutboundUplink":true,"statsOutboundDownlink":true}},"inbounds":[{"tag":"forward","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":%q,"address":"127.0.0.1","port":%d}}],"outbounds":[{"tag":"proxy","protocol":"vmess","settings":{"address":"127.0.0.1","port":%d,"id":"2b831278-cc9f-42a8-a52a-28ac96ea41c3","security":"aes-128-gcm"},"mux":{"enabled":true,"concurrency":8,"xudpConcurrency":8,"xudpProxyUDP443":"allow"}}]}`, listen, network, targetPort, serverPort))
			conn, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", listen), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			exchange(t, conn, []byte("metered mux payload"))
			manager := client.GetFeature(stats.ManagerType()).(*appstats.Manager)
			for _, direction := range []string{"uplink", "downlink"} {
				counter := manager.GetCounter("outbound>>>proxy>>>traffic>>>" + direction)
				if counter == nil {
					t.Fatalf("missing real %s counter", direction)
				}
				if counter.Value() < 19 {
					t.Fatalf("real %s wire counter = %d, want at least 19", direction, counter.Value())
				}
			}
			handler := client.GetFeature(outbound.ManagerType()).(outbound.Manager).GetHandler("proxy")
			if err := handler.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			final, err := manager.SealCounters(ctx)
			if err != nil {
				t.Fatalf("closing %s mux left metered IO alive: %v", network, err)
			}
			again, err := manager.SealCounters(ctx)
			if err != nil || !reflect.DeepEqual(final, again) {
				t.Fatalf("mux final counters changed: %v / %v, %v", final, again, err)
			}
			if network == "tcp" {
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				_, err := conn.Read(make([]byte, 1))
				var timeout net.Error
				if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
					t.Fatalf("closed mux retained its TCP client: %v", err)
				}
			}
		})
	}
}
