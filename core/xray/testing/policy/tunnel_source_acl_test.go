package policy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/infra/conf"
)

func TestTunnelSourceACLChecksPeerBeforeDispatchAndBilling(t *testing.T) {
	for _, family := range []string{"tcp4", "tcp6"} {
		t.Run(family, func(t *testing.T) {
			host, allow, deny, udpNetwork := "127.0.0.1", "127.0.0.1/32", "127.0.0.2/32", "udp4"
			if family == "tcp6" {
				host, allow, deny, udpNetwork = "::1", "::1/128", "2001:db8::/32", "udp6"
			}
			var tcpCount, udpCount atomic.Int32
			target, err := net.Listen(family, net.JoinHostPort(host, "0"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = target.Close() })
			go func() {
				for {
					c, err := target.Accept()
					if err != nil {
						return
					}
					tcpCount.Add(1)
					go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
				}
			}()
			targetPort := target.Addr().(*net.TCPAddr).Port
			udp, err := net.ListenPacket(udpNetwork, net.JoinHostPort(host, fmt.Sprint(targetPort)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = udp.Close() })
			go func() {
				b := make([]byte, 1024)
				for {
					n, addr, err := udp.ReadFrom(b)
					if err != nil {
						return
					}
					udpCount.Add(1)
					_, _ = udp.WriteTo(b[:n], addr)
				}
			}()
			reserve, err := net.Listen(family, net.JoinHostPort(host, "0"))
			if err != nil {
				t.Fatal(err)
			}
			listen := reserve.Addr().(*net.TCPAddr).Port
			_ = reserve.Close()
			reserve, err = net.Listen(family, net.JoinHostPort(host, "0"))
			if err != nil {
				t.Fatal(err)
			}
			siblingPort := reserve.Addr().(*net.TCPAddr).Port
			_ = reserve.Close()
			owned := func(prefix string) string {
				return fmt.Sprintf(`{"tag":"acl","listen":%q,"port":%d,"protocol":"tunnel","settings":{"allowedNetwork":"tcp,udp","rewriteAddress":%q,"rewritePort":%d,"clientId":"acl-owner","allowedSourceCidrs":[%q]}}`, host, listen, host, targetPort, prefix)
			}
			sibling := fmt.Sprintf(`{"tag":"sibling","listen":%q,"port":%d,"protocol":"tunnel","settings":{"allowedNetwork":"tcp","rewriteAddress":%q,"rewritePort":%d,"clientId":"acl-owner","allowedSourceCidrs":[%q]}}`, host, siblingPort, host, targetPort, allow)
			instance := start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"clientPolicy":{"policies":[{"clientId":"acl-owner","version":1,"enabled":true,"multiplierMicros":1500000,"burstBytes":65536}]},"inbounds":[%s,%s],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow"}]}}]}`, owned(allow), sibling))
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			dial := func(network, source string, listen int) net.Conn {
				t.Helper()
				d := net.Dialer{Timeout: time.Second}
				if network == family {
					d.LocalAddr = &net.TCPAddr{IP: net.ParseIP(source)}
				} else {
					d.LocalAddr = &net.UDPAddr{IP: net.ParseIP(source)}
				}
				c, err := d.Dial(network, net.JoinHostPort(host, fmt.Sprint(listen)))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				return c
			}
			tcpFlow, udpFlow := dial(family, host, listen), dial(udpNetwork, host, listen)
			survivor := dial(family, host, siblingPort)
			exchange(t, tcpFlow, []byte("tcp-ok"))
			exchange(t, udpFlow, []byte("udp-ok"))
			exchange(t, survivor, []byte("siblng"))
			before, err := engine.Snapshot("acl-owner")
			if err != nil || before.Usage != (clientpolicy.Usage{RawUpload: 18, RawDownload: 18, BilledBytes: 54}) {
				t.Fatalf("allowed traffic: %+v %v", before, err)
			}
			rejected := func(c net.Conn) {
				t.Helper()
				_ = c.SetDeadline(time.Now().Add(200 * time.Millisecond))
				_, _ = c.Write([]byte("must-not-arrive"))
				if _, err := c.Read(make([]byte, 32)); err == nil {
					t.Fatal("denied source received payload")
				}
			}
			if family == "tcp4" {
				rejected(dial(family, "127.0.0.2", listen))
				rejected(dial(udpNetwork, "127.0.0.2", listen))
			}
			manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
			if err := manager.RemoveHandler(context.Background(), "acl"); err != nil {
				t.Fatal(err)
			}
			_ = tcpFlow.SetReadDeadline(time.Now().Add(time.Second))
			_, err = tcpFlow.Read(make([]byte, 1))
			var ne net.Error
			if err == nil || errors.As(err, &ne) && ne.Timeout() {
				t.Fatalf("replaced TCP flow stayed open: %v", err)
			}
			var cfg conf.InboundDetourConfig
			if err := json.Unmarshal([]byte(owned(deny)), &cfg); err != nil {
				t.Fatal(err)
			}
			pb, err := cfg.Build()
			if err != nil {
				t.Fatal(err)
			}
			if err := core.AddInboundHandler(instance, pb); err != nil {
				t.Fatal(err)
			}
			rejected(udpFlow)
			rejected(dial(family, host, listen))
			rejected(dial(udpNetwork, host, listen))
			after, err := engine.Snapshot("acl-owner")
			if err != nil || after.Usage != before.Usage || after.ActiveSessions != 1 {
				t.Fatalf("denied source consumed quota or opened a managed session: %+v %v", after, err)
			}
			exchange(t, survivor, []byte("still!"))
			final, err := engine.Snapshot("acl-owner")
			if err != nil || final.Usage != (clientpolicy.Usage{RawUpload: 24, RawDownload: 24, BilledBytes: 72}) {
				t.Fatalf("sibling flow was disrupted or billing changed: %+v %v", final, err)
			}
			if tcpCount.Load() != 2 || udpCount.Load() != 1 {
				t.Fatalf("denied source reached target: TCP=%d UDP=%d", tcpCount.Load(), udpCount.Load())
			}
		})
	}
}
