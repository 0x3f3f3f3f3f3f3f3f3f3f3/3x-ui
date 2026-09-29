package policy_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/proxy"
)

func TestAuthenticatedProtocolsShareTunnelIdentityAndDisconnect(t *testing.T) {
	const credential = "936997e1-3b0c-4de9-9eea-047ee5829d3e"
	cases := []struct {
		name     string
		inbound  string
		outbound string
		mux      bool
	}{
		{
			name:     "vless",
			inbound:  `{"clients":[{"id":"` + credential + `","email":"vless@example.test","clientId":"owner"}],"decryption":"none"}`,
			outbound: `{"vnext":[{"address":"127.0.0.1","port":%d,"users":[{"id":"` + credential + `","encryption":"none"}]}]}`,
		},
		{
			name:     "vmess",
			inbound:  `{"clients":[{"id":"` + credential + `","email":"vmess@example.test","clientId":"owner"}]}`,
			outbound: `{"vnext":[{"address":"127.0.0.1","port":%d,"users":[{"id":"` + credential + `","security":"auto"}]}]}`,
		},
		{
			name:     "trojan",
			inbound:  `{"clients":[{"password":"test-secret","email":"trojan@example.test","clientId":"owner"}]}`,
			outbound: `{"servers":[{"address":"127.0.0.1","port":%d,"password":"test-secret"}]}`,
		},
		{
			name:     "shadowsocks",
			inbound:  `{"clients":[{"method":"chacha20-ietf-poly1305","password":"test-secret","email":"ss@example.test","clientId":"owner"}],"network":"tcp,udp"}`,
			outbound: `{"servers":[{"address":"127.0.0.1","port":%d,"method":"chacha20-ietf-poly1305","password":"test-secret"}]}`,
		},
	}
	for _, tc := range cases {
		tc.mux = true
		cases = append(cases, tc)
	}
	for _, tc := range cases {
		mode := "plain"
		if tc.mux {
			mode = "mux"
		}
		t.Run(tc.name+"/"+mode, func(t *testing.T) {
			echo := tcpEcho(t)
			target := echo.Addr().(*net.TCPAddr).Port
			udpEcho, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", target))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = udpEcho.Close() })
			go func() {
				b := make([]byte, 1024)
				for {
					n, peer, err := udpEcho.ReadFrom(b)
					if err != nil {
						return
					}
					_, _ = udpEcho.WriteTo(b[:n], peer)
				}
			}()
			authPort, tunnelPort, peerPort := port(t), port(t), port(t)
			server := start(t, fmt.Sprintf(`{
			 "log":{"loglevel":"error"},
			 "clientPolicy":{"policies":[{"clientId":"owner","version":1,"enabled":true,"multiplierMicros":1500000,"burstBytes":65536}]},
			 "inbounds":[
			 {"tag":"authenticated","listen":"127.0.0.1","port":%d,"protocol":%q,"settings":%s},
			 {"tag":"owned-tunnel","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"owner"}}],
			 "outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]
			}`, authPort, tc.name, tc.inbound, tunnelPort, target))
			start(t, fmt.Sprintf(`{
			 "log":{"loglevel":"error"},
			 "inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp,udp","address":"127.0.0.1","port":%d}}],
			 "outbounds":[{"protocol":%q,"settings":%s,"mux":{"enabled":%t,"concurrency":8}}]
			}`, peerPort, target, tc.name, fmt.Sprintf(tc.outbound, authPort), tc.mux))
			dial := func(network string, p int) net.Conn {
				t.Helper()
				c, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", p), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				return c
			}
			first, second, tunnel := dial("tcp", peerPort), dial("tcp", peerPort), dial("tcp", tunnelPort)
			udp := dial("udp", peerPort)
			exchange(t, first, bytes.Repeat([]byte{1}, 257))
			exchange(t, second, bytes.Repeat([]byte{2}, 31))
			exchange(t, tunnel, bytes.Repeat([]byte{3}, 17))
			exchange(t, udp, bytes.Repeat([]byte{4}, 13))
			engine := server.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			snap, err := engine.Snapshot("owner")
			if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 318, RawDownload: 318, BilledBytes: 954}) || snap.ActiveSessions != 4 {
				t.Fatalf("authenticated payload did not share Tunnel identity: %+v, %v", snap, err)
			}
			policy, _, err := engine.GetClient("owner")
			if err != nil {
				t.Fatal(err)
			}
			policy.Version++
			policy.UploadRate = 1
			if err := engine.Apply(policy); err != nil {
				t.Fatal(err)
			}
			exchange(t, first, bytes.Repeat([]byte{5}, 65536))
			pending := make(chan error, 1)
			_ = tunnel.SetDeadline(time.Now().Add(3 * time.Second))
			go func() {
				payload := bytes.Repeat([]byte{6}, 13)
				if _, err := tunnel.Write(payload); err != nil {
					pending <- err
					return
				}
				reply := make([]byte, len(payload))
				_, err := io.ReadFull(tunnel, reply)
				if err == nil && !bytes.Equal(reply, payload) {
					err = fmt.Errorf("queued Tunnel reply differs from its payload")
				}
				pending <- err
			}()
			select {
			case err := <-pending:
				t.Fatalf("Tunnel did not share the exhausted authenticated upload bucket: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			policy.Version++
			policy.UploadRate = 0
			policy.Multiplier = 500000
			changed := time.Now()
			if err := engine.Apply(policy); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-pending:
				if err != nil || time.Since(changed) > 2*time.Second {
					t.Fatalf("hot update failed to release the shared bucket within two seconds: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("hot update did not release the queued Tunnel payload")
			}
			snap, err = engine.Snapshot("owner")
			if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 65867, RawDownload: 65867, BilledBytes: 197575}) {
				t.Fatalf("rate or multiplier change rewrote historical usage: %+v, %v", snap, err)
			}
			manager := server.GetFeature(inbound.ManagerType()).(inbound.Manager)
			handler, err := manager.GetHandler(context.Background(), "authenticated")
			if err != nil {
				t.Fatal(err)
			}
			users := handler.(proxy.GetInbound).GetInbound().(proxy.UserManager).GetUsers(context.Background())
			if len(users) != 1 {
				t.Fatalf("expected one authenticated credential, got %d", len(users))
			}
			if err := (&command.RemoveUserOperation{Email: users[0].Email}).ApplyInbound(context.Background(), handler); err != nil {
				t.Fatal(err)
			}
			for _, c := range []net.Conn{first, second} {
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				if _, err := c.Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
					t.Fatalf("credential removal left an existing protocol stream alive: %v", err)
				}
			}
			_ = udp.SetDeadline(time.Now().Add(200 * time.Millisecond))
			_, _ = udp.Write([]byte("removed"))
			if n, err := udp.Read(make([]byte, 32)); n != 0 || err == nil {
				t.Fatalf("removed credential returned UDP payload: n=%d err=%v", n, err)
			}
			exchange(t, tunnel, []byte("survivor"))
			snap, err = engine.Snapshot("owner")
			if err != nil || snap.ActiveSessions != 1 || snap.Usage != (clientpolicy.Usage{RawUpload: 65875, RawDownload: 65875, BilledBytes: 197583}) {
				t.Fatalf("credential removal affected sibling identity or usage: %+v, %v", snap, err)
			}
			policy.Version++
			policy.Enabled = false
			deadline := time.Now().Add(2 * time.Second)
			if err := engine.Apply(policy); err != nil {
				t.Fatal(err)
			}
			for _, c := range []net.Conn{first, second, tunnel} {
				_ = c.SetReadDeadline(deadline)
				if _, err := c.Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
					t.Fatalf("disable left an existing protocol stream alive: %v", err)
				}
			}
			_ = udp.SetDeadline(time.Now().Add(200 * time.Millisecond))
			_, _ = udp.Write([]byte("disabled"))
			if n, err := udp.Read(make([]byte, 32)); n != 0 || err == nil {
				t.Fatalf("disabled UDP session returned payload: n=%d err=%v", n, err)
			}
			snap, err = engine.Snapshot("owner")
			if err != nil || snap.ActiveSessions != 0 || snap.Reasons != clientpolicy.ReasonDisabled || snap.Usage.BilledBytes != 197583 {
				t.Fatalf("disable altered shared accounting or retained sessions: %+v, %v", snap, err)
			}
		})
	}
}
