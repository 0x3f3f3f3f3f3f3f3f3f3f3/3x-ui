package policy_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/trafficcontrol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestPrivateFinalDrainClosesRealLegacyTraffic(t *testing.T) {
	for _, kind := range []string{"direct", "vmess", "mux"} {
		for _, network := range []string{"tcp", "udp"} {
			t.Run(kind+"/"+network, func(t *testing.T) {
				target := tcpEcho(t)
				targetPort := target.Addr().(*net.TCPAddr).Port
				if network == "udp" {
					packet, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", targetPort))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { packet.Close() })
					go func() {
						data := make([]byte, 4096)
						for {
							n, addr, err := packet.ReadFrom(data)
							if err != nil {
								return
							}
							packet.WriteTo(data[:n], addr)
						}
					}()
				}
				outbound := `{"tag":"proxy","protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}`
				if kind != "direct" {
					serverPort := port(t)
					start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"vmess","settings":{"clients":[{"id":"2b831278-cc9f-42a8-a52a-28ac96ea41c3"}]}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, serverPort))
					outbound = fmt.Sprintf(`{"tag":"proxy","protocol":"vmess","settings":{"address":"127.0.0.1","port":%d,"id":"2b831278-cc9f-42a8-a52a-28ac96ea41c3","security":"aes-128-gcm"},"mux":{"enabled":%t,"concurrency":8,"xudpConcurrency":8}}`, serverPort, kind == "mux")
				}
				socket, listen := privateSocket(t), port(t)
				start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"trafficControl":{"listen":%q},"stats":{},"policy":{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}},"system":{"statsInboundUplink":true,"statsInboundDownlink":true,"statsOutboundUplink":true,"statsOutboundDownlink":true}},"inbounds":[{"tag":"forward","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":%q,"address":"127.0.0.1","port":%d,"email":"legacy-owner"}}],"outbounds":[%s]}`, socket, listen, network, targetPort, outbound))
				connection, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Close()
				api := trafficcontrol.NewTrafficControlServiceClient(connection)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				caps, err := api.GetCapabilities(ctx, &trafficcontrol.Empty{})
				if err != nil {
					t.Fatal(err)
				}
				address := fmt.Sprintf("127.0.0.1:%d", listen)
				conn, err := net.DialTimeout(network, address, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err := api.Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: "stale-boot"}); status.Code(err) != codes.Aborted {
					t.Fatalf("stale boot: %v", err)
				}
				payload := []byte("final legacy payload")
				exchange(t, conn, payload)
				final, err := api.Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: caps.BootId})
				if err != nil || final.GetBootId() != caps.BootId {
					t.Fatalf("final drain: %v %v", final, err)
				}
				for _, direction := range []string{"uplink", "downlink"} {
					if got := final.Counters["user>>>legacy-owner>>>traffic>>>"+direction]; got != int64(len(payload)) {
						t.Fatalf("user %s=%d, want %d: %v", direction, got, len(payload), final.Counters)
					}
					if got := final.Counters["outbound>>>proxy>>>traffic>>>"+direction]; got < int64(len(payload)) {
						t.Fatalf("outbound %s=%d", direction, got)
					}
				}
				again, err := api.Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: caps.BootId})
				if err != nil || !reflect.DeepEqual(final, again) {
					t.Fatalf("final reply changed: %v", err)
				}
				if network == "tcp" {
					conn.SetReadDeadline(time.Now().Add(time.Second))
					_, err = conn.Read(make([]byte, 1))
					var timeout net.Error
					if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
						t.Fatalf("business TCP survived final acknowledgement: %v", err)
					}
					listener, err := net.Listen("tcp", address)
					if err != nil {
						t.Fatalf("listener retained: %v", err)
					}
					listener.Close()
				} else {
					listener, err := net.ListenPacket("udp", address)
					if err != nil {
						t.Fatalf("UDP listener retained: %v", err)
					}
					listener.Close()
				}
			})
		}
	}
}
