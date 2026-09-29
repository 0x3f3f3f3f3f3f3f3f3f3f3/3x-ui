package policy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/infra/conf"
)

func TestLegacyTunnelRemovalClosesTCPAndUnixConnections(t *testing.T) {
	for _, network := range []string{"tcp", "unix"} {
		t.Run(network, func(t *testing.T) {
			target := tcpEcho(t)
			listen := port(t)
			address := fmt.Sprintf("127.0.0.1:%d", listen)
			binding := fmt.Sprintf(`"listen":"127.0.0.1","port":%d`, listen)
			if network == "unix" {
				address = filepath.Join(t.TempDir(), "forward.sock")
				binding = fmt.Sprintf(`"listen":%q`, address)
			}
			instance := start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"inbounds":[{"tag":"legacy",%s,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, binding, target.Addr().(*net.TCPAddr).Port))
			conn, err := net.DialTimeout(network, address, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			exchange(t, conn, []byte("legacy forwarding"))
			manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
			if err := manager.RemoveHandler(context.Background(), "legacy"); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, err = conn.Read(make([]byte, 1))
			var netErr net.Error
			if err == nil || errors.As(err, &netErr) && netErr.Timeout() {
				t.Fatalf("legacy listener retained an established %s stream: %v", network, err)
			}
		})
	}
}

func TestTunnelRemovalDrainsOldTCPAndUDPSessionsBeforeReassignment(t *testing.T) {
	target := tcpEcho(t)
	targetPort := target.Addr().(*net.TCPAddr).Port
	udpTarget, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", targetPort))
	if err != nil {
		t.Fatal(err)
	}
	defer udpTarget.Close()
	go func() {
		b := make([]byte, 1024)
		for {
			n, remote, err := udpTarget.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = udpTarget.WriteTo(b[:n], remote)
		}
	}()
	listen, other := port(t), port(t)
	owned := func(owner string) string {
		return fmt.Sprintf(`{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp,udp","address":"127.0.0.1","port":%d,"clientId":%q}}`, listen, targetPort, owner)
	}
	instance := start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"clientPolicy":{"policies":[{"clientId":"a","version":1,"enabled":true,"multiplierMicros":1000000,"burstBytes":65536},{"clientId":"b","version":1,"enabled":true,"multiplierMicros":1000000,"burstBytes":65536}]},"inbounds":[%s,{"tag":"survivor","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"a"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, owned("a"), other, targetPort))
	dial := func(network string, p int) net.Conn {
		t.Helper()
		conn, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", p), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	oldTCP, oldUDP, survivor := dial("tcp", listen), dial("udp", listen), dial("tcp", other)
	exchange(t, oldTCP, bytes.Repeat([]byte{1}, 257))
	exchange(t, oldUDP, bytes.Repeat([]byte{2}, 31))
	exchange(t, survivor, bytes.Repeat([]byte{3}, 17))
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	before, err := engine.Snapshot("a")
	if err != nil || before.ActiveSessions != 3 || before.Usage.BilledBytes != 610 {
		t.Fatalf("initial shared owner state: %+v, %v", before, err)
	}
	manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
	if err := manager.RemoveHandler(context.Background(), "owned"); err != nil {
		t.Fatal(err)
	}
	_ = oldTCP.SetReadDeadline(time.Now().Add(time.Second))
	_, err = oldTCP.Read(make([]byte, 1))
	var netErr net.Error
	if err == nil || errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("removed listener retained its old TCP connection: %v", err)
	}
	remaining, err := engine.Snapshot("a")
	if err != nil || remaining.ActiveSessions != 1 {
		t.Fatalf("handler removal returned before TCP/UDP sessions drained: %+v, %v", remaining, err)
	}
	exchange(t, survivor, bytes.Repeat([]byte{4}, 7))
	var replacement conf.InboundDetourConfig
	if err := json.Unmarshal([]byte(owned("b")), &replacement); err != nil {
		t.Fatal(err)
	}
	pb, err := replacement.Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.AddInboundHandler(instance, pb); err != nil {
		t.Fatal(err)
	}
	exchange(t, dial("tcp", listen), bytes.Repeat([]byte{5}, 64))
	exchange(t, dial("udp", listen), bytes.Repeat([]byte{6}, 13))
	a, err := engine.Snapshot("a")
	if err != nil || a.Usage.RawUpload != 312 || a.Usage.RawDownload != 312 || a.Usage.BilledBytes != 624 || a.ActiveSessions != 1 {
		t.Fatalf("reassignment altered the previous owner or sibling listener: %+v, %v", a, err)
	}
	b, err := engine.Snapshot("b")
	if err != nil || b.Usage.RawUpload != 77 || b.Usage.RawDownload != 77 || b.Usage.BilledBytes != 154 {
		t.Fatalf("reassigned listener inherited old accounting: %+v, %v", b, err)
	}
}
