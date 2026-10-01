package mieru_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	miCommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/xtls/xray-core/app/clientpolicy"
)

func TestNativeMieruDisableAndExpiryCloseTCPAndUDP(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		for _, payloadMode := range []string{"tcp", "udp"} {
			for _, action := range []string{"disable", "expire"} {
				t.Run(mode+"/"+payloadMode+"/"+action, func(t *testing.T) {
					port := reservePort(t, mode)
					instance := nativeCore(t, port, mode)
					client := referenceClient(t, port, mode)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					target := echoTarget(t, payloadMode)
					conn, err := client.DialContext(ctx, target)
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					payload := []byte("before-policy-close")
					if payloadMode == "tcp" {
						exchangeNative(t, conn, string(payload))
					} else {
						packets := miCommon.NewUDPAssociateWrapper(miCommon.NewPacketOverStreamTunnel(conn))
						if _, err := packets.WriteTo(payload, target); err != nil {
							t.Fatal(err)
						}
						_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
						result := make([]byte, 100)
						n, _, err := packets.ReadFrom(result)
						if err != nil || !bytes.Equal(result[:n], payload) {
							t.Fatalf("UDP payload %q/%v", result[:n], err)
						}
					}
					manager := instance.GetFeature((*clientpolicy.Manager)(nil)).(clientpolicy.Manager)
					policy := clientpolicy.Policy{ClientID: nativeClientID, Version: 2, Enabled: true, Multiplier: 1500000, BurstBytes: 65536}
					wantReason := clientpolicy.ReasonDisabled
					if action == "disable" {
						policy.Enabled = false
					} else {
						policy.ExpiresAt = time.Now().Add(120 * time.Millisecond).UnixMilli()
						wantReason = clientpolicy.ReasonExpired
					}
					if err := manager.Apply(policy); err != nil {
						t.Fatal(err)
					}
					_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
					if _, err := conn.Read(make([]byte, 1)); err == nil || nativeIsTimeout(err) {
						t.Fatalf("policy %s did not close active %s: %v", action, payloadMode, err)
					}
					snapshot, err := manager.Snapshot(nativeClientID)
					want := clientpolicy.Usage{RawUpload: uint64(len(payload)), RawDownload: uint64(len(payload)), BilledBytes: uint64(len(payload) * 3)}
					if err != nil || snapshot.Reasons&wantReason == 0 || snapshot.Usage != want {
						t.Fatalf("policy closure ledger/reason %+v/%v want %+v", snapshot, err, want)
					}
				})
			}
		}
	}
}

func TestNativeMieruAndTunnelShareUploadAndDownloadRate(t *testing.T) {
	for _, mode := range []string{"TCP", "UDP"} {
		t.Run(mode, func(t *testing.T) {
			target := echoTarget(t, "tcp").(*net.TCPAddr)
			port, tunnelPort := reservePort(t, mode), reservePort(t, "TCP")
			instance := nativeCoreConfigured(t, port, mode, func(config map[string]any) {
				config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "rate-tunnel", "listen": "127.0.0.1", "port": tunnelPort, "protocol": "dokodemo-door", "settings": map[string]any{"network": "tcp", "address": "127.0.0.1", "port": target.Port, "clientId": nativeClientID, "email": "canonical-alice", "allowedSourceCidrs": []string{"127.0.0.0/8"}}})
			})
			client := referenceClient(t, port, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			tunnel, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort))
			if err != nil {
				t.Fatal(err)
			}
			defer tunnel.Close()
			manager := instance.GetFeature((*clientpolicy.Manager)(nil)).(clientpolicy.Manager)
			policy := clientpolicy.Policy{ClientID: nativeClientID, Version: 2, Enabled: true, Multiplier: 1500000, BurstBytes: 8192, UploadRate: 32768}
			payload := bytes.Repeat([]byte{0x63}, 16384)
			for _, direction := range []string{"upload", "download"} {
				if direction == "download" {
					policy.Version++
					policy.UploadRate, policy.DownloadRate = 0, 32768
				}
				if err := manager.Apply(policy); err != nil {
					t.Fatal(err)
				}
				results := make(chan error, 2)
				start := time.Now()
				for _, active := range []net.Conn{conn, tunnel} {
					go func(active net.Conn) {
						_ = active.SetDeadline(time.Now().Add(5 * time.Second))
						if _, err := active.Write(payload); err != nil {
							results <- err
							return
						}
						result := make([]byte, len(payload))
						_, err := io.ReadFull(active, result)
						if err == nil && !bytes.Equal(result, payload) {
							err = fmt.Errorf("rate-limited payload corruption")
						}
						results <- err
					}(active)
				}
				for range 2 {
					if err := <-results; err != nil {
						t.Fatal(err)
					}
				}
				if elapsed := time.Since(start); elapsed < 650*time.Millisecond {
					t.Fatalf("%s rate bypassed aggregate identity: %v for 32768 bytes with 32768B/s and 8192B burst", direction, elapsed)
				}
			}
			snapshot, err := manager.Snapshot(nativeClientID)
			want := clientpolicy.Usage{RawUpload: 65536, RawDownload: 65536, BilledBytes: 196608}
			if err != nil || snapshot.Usage != want {
				t.Fatalf("rate ledger %+v want %+v: %v", snapshot.Usage, want, err)
			}
		})
	}
}
