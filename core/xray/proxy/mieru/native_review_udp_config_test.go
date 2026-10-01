package mieru_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	miCommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/xtls/xray-core/infra/conf"
)

func TestReviewUDPAssociateMayUseUnspecifiedTarget(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			port := reservePort(t, mode)
			nativeCore(t, port, mode)
			client := referenceClient(t, port, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			stop := context.AfterFunc(ctx, func() { _ = client.Stop() })
			defer stop()
			conn, err := client.DialContext(ctx, &net.UDPAddr{IP: net.IPv4zero, Port: 0})
			if err != nil {
				t.Fatalf("official-library UDP association with standard unspecified target was rejected: %v", err)
			}
			defer conn.Close()
			target := echoTarget(t, "udp")
			tunnel := miCommon.NewUDPAssociateWrapper(miCommon.NewPacketOverStreamTunnel(conn))
			if _, err = tunnel.WriteTo([]byte("standard-udp-associate"), target); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, 64)
			n, _, err := tunnel.ReadFrom(response)
			if err != nil || string(response[:n]) != "standard-udp-associate" {
				t.Fatalf("UDP echo: %q %v", response[:n], err)
			}
		})
	}
}

func TestReviewNativeStreamWrappersAreRejected(t *testing.T) {
	for _, stream := range []string{
		`{"network":"tcp","tcpSettings":{"header":{"type":"http"}}}`,
		`{"network":"tcp","tcpSettings":{"acceptProxyProtocol":true}}`,
	} {
		t.Run(stream, func(t *testing.T) {
			raw := fmt.Sprintf(`{"inbounds":[{"listen":"127.0.0.1","port":38001,"protocol":"mieru","streamSettings":%s,"settings":{"users":[{"username":"alice","password":"secret","clientId":"alice-id","email":"alice"}]}}]}`, stream)
			var config conf.Config
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Build(); err == nil {
				t.Fatal("native mieru accepted TCP framing setting that it never applies")
			}
		})
	}
}

func TestReviewEmptyUDPDatagramReachesTarget(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			port := reservePort(t, mode)
			nativeCore(t, port, mode)
			client := referenceClient(t, port, mode)
			target, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, target.LocalAddr())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			tunnel := miCommon.NewUDPAssociateWrapper(miCommon.NewPacketOverStreamTunnel(conn))
			if _, err = tunnel.WriteTo(nil, target.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			_ = target.SetReadDeadline(time.Now().Add(time.Second))
			n, _, err := target.ReadFrom(make([]byte, 1))
			if err != nil || n != 0 {
				t.Fatalf("valid empty UDP datagram never reached target: %d %v", n, err)
			}
		})
	}
}
