package policy_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	_ "github.com/xtls/xray-core/main/distro/all"
)

func tcpEcho(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return l
}

func TestTunnelSelectedSocksOutboundAndBlockHaveNoDirectFallback(t *testing.T) {
	var accepted atomic.Int32
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	proxyPort, forwardPort, blockedPort := port(t), port(t), port(t)
	target := echo.Addr().(*net.TCPAddr).Port
	s := start(t, fmt.Sprintf(`{
	 "log":{"loglevel":"error"},"clientPolicy":{"policies":[{"clientId":"owner","version":1,"enabled":true,"multiplierMicros":1500000,"burstBytes":65536}]},
	 "inbounds":[
	 {"tag":"upstream","listen":"127.0.0.1","port":%d,"protocol":"mixed","settings":{"auth":"noauth"}},
	 {"tag":"forward","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":%d,"clientId":"owner"}},
	 {"tag":"denied","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":%d,"clientId":"owner"}}],
	 "outbounds":[{"tag":"block","protocol":"blackhole"},{"tag":"proxy","protocol":"socks","settings":{"address":"127.0.0.1","port":%d}},
	 {"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}],
	 "routing":{"rules":[{"type":"field","inboundTag":["forward"],"outboundTag":"proxy"},{"type":"field","inboundTag":["upstream"],"outboundTag":"direct"}]}
	}`, proxyPort, forwardPort, target, blockedPort, target, proxyPort))
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", forwardPort))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	exchange(t, c, bytes.Repeat([]byte{7}, 4096))
	e := s.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	snap, _ := e.Snapshot("owner")
	if snap.Usage != (clientpolicy.Usage{RawUpload: 4096, RawDownload: 4096, BilledBytes: 12288}) {
		t.Fatalf("proxy chain bypassed or duplicated accounting: %+v", snap)
	}
	blocked, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", blockedPort))
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	blocked.SetDeadline(time.Now().Add(300 * time.Millisecond))
	blocked.Write([]byte("must not arrive"))
	if _, err := blocked.Read(make([]byte, 20)); err == nil {
		t.Fatal("blocked route returned data")
	}
	if accepted.Load() != 1 {
		t.Fatalf("blocked route silently dialed target: accepted=%d", accepted.Load())
	}
}

func TestTunnelManagedIdentityWithoutPolicyFailsClosed(t *testing.T) {
	var accepted atomic.Int32
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	listen := port(t)
	start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"missing"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow"}]}}]}`, listen, echo.Addr().(*net.TCPAddr).Port))
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write([]byte("denied"))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("unconfigured managed identity returned data")
	}
	if accepted.Load() != 0 {
		t.Fatal("unconfigured managed identity dialed target")
	}
}

func TestTunnelQuotaStopsExistingFlowsAndReconnectAtHundredMiB(t *testing.T) {
	echo := tcpEcho(t)
	listen := port(t)
	s := start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"clientPolicy":{"policies":[{"clientId":"quota","version":1,"enabled":true,"multiplierMicros":2000000,"quotaBytes":104857600,"burstBytes":65536}]},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"quota"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, listen, echo.Addr().(*net.TCPAddr).Port))
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{8}, 16384)
	reply := make([]byte, len(payload))
	var sent, received uint64
	for i := 0; i < 1700; i++ {
		n, err := c.Write(payload)
		sent += uint64(n)
		if err != nil {
			break
		}
		n, err = io.ReadFull(c, reply)
		received += uint64(n)
		if err != nil {
			break
		}
		if !bytes.Equal(payload, reply) {
			t.Fatal("quota stream corrupted")
		}
	}
	e := s.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	snap, _ := e.Snapshot("quota")
	if snap.Reasons != clientpolicy.ReasonQuota || snap.ActiveSessions != 0 || snap.Usage.BilledBytes != 104857600 || snap.Usage.RawUpload+snap.Usage.RawDownload != 52428800 {
		t.Fatalf("quota did not stop at exact admitted budget: %+v sent=%d received=%d", snap, sent, received)
	}
	if received > snap.Usage.RawDownload || snap.Usage.RawDownload-received > 65536 || sent < snap.Usage.RawUpload || sent-snap.Usage.RawUpload > 65536 {
		t.Fatalf("endpoint/admission uncertainty exceeded 64KiB: %+v sent=%d received=%d", snap, sent, received)
	}
	t.Logf("quota=104857600 multiplier=2 admittedUp=%d admittedDown=%d billed=%d senderWritten=%d receiverRead=%d", snap.Usage.RawUpload, snap.Usage.RawDownload, snap.Usage.BilledBytes, sent, received)
	reconnect, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	defer reconnect.Close()
	reconnect.SetDeadline(time.Now().Add(time.Second))
	reconnect.Write([]byte("denied"))
	if _, err := reconnect.Read(make([]byte, 1)); err == nil {
		t.Fatal("exhausted user reconnected")
	}
	after, _ := e.Snapshot("quota")
	if after.Usage != snap.Usage {
		t.Fatalf("reconnect obtained extra budget: before=%+v after=%+v", snap, after)
	}
}

func port(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func start(t *testing.T, config string) *core.Instance {
	t.Helper()
	var c conf.Config
	if err := json.Unmarshal([]byte(config), &c); err != nil {
		t.Fatal(err)
	}
	pb, err := c.Build()
	if err != nil {
		t.Fatal(err)
	}
	s, err := core.New(pb)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	return s
}

func exchange(t *testing.T, c net.Conn, payload []byte) {
	t.Helper()
	if err := c.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("forwarded payload corrupted")
	}
}

func TestTunnelTCPAndUDPOwnersShareLivePolicyThroughDispatcher(t *testing.T) {
	echo := tcpEcho(t)
	target := echo.Addr().(*net.TCPAddr).Port
	u, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", target))
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	go func() {
		b := make([]byte, 65536)
		for {
			n, addr, err := u.ReadFrom(b)
			if err != nil {
				return
			}
			u.WriteTo(b[:n], addr)
		}
	}()
	a, b := port(t), port(t)
	s := start(t, fmt.Sprintf(`{
	 "log":{"loglevel":"debug"},
	 "clientPolicy":{"policies":[{"clientId":"stable-owner","version":1,"enabled":true,"multiplierMicros":2000000,"quotaBytes":10240,"burstBytes":65536}]},
	 "inbounds":[
	  {"tag":"a","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"allowedNetwork":"tcp,udp","rewriteAddress":"127.0.0.1","rewritePort":%d,"clientId":"stable-owner","email":"legacy-email"}},
	  {"tag":"b","listen":"127.0.0.1","port":%d,"protocol":"dokodemo-door","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"stable-owner"}}],
	 "outbounds":[{"tag":"blocked","protocol":"blackhole"},{"tag":"selected","protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}],
	 "routing":{"rules":[{"type":"field","inboundTag":["a","b"],"outboundTag":"selected"}]}
	}`, a, target, b, target))
	feature := s.GetFeature((*clientpolicy.Manager)(nil))
	if feature == nil {
		t.Fatal("managed policy feature missing from real core")
	}
	e := feature.(*clientpolicy.Engine)
	c1, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", a))
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", b))
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	cu, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", a))
	if err != nil {
		t.Fatal(err)
	}
	defer cu.Close()
	exchange(t, c1, bytes.Repeat([]byte{1}, 512))
	exchange(t, c2, bytes.Repeat([]byte{2}, 512))
	exchange(t, cu, bytes.Repeat([]byte{3}, 1024))
	snap, err := e.Snapshot("stable-owner")
	if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 2048, RawDownload: 2048, BilledBytes: 8192}) || snap.ActiveSessions != 3 {
		t.Fatalf("real payload bypass/double billing: %+v %v", snap, err)
	}
	p := clientpolicy.Policy{ClientID: "stable-owner", Version: 2, Enabled: false, Multiplier: 2000000, QuotaBytes: 10240, BurstBytes: 65536}
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	for _, c := range []net.Conn{c1, c2} {
		c.SetReadDeadline(time.Now().Add(time.Second))
		_, err := c.Read(make([]byte, 1))
		if err == nil {
			t.Fatal("disabled active TCP connection survived")
		}
		if n, ok := err.(net.Error); ok && n.Timeout() {
			t.Fatal("disable did not close idle TCP socket")
		}
	}
	cu.SetDeadline(time.Now().Add(200 * time.Millisecond))
	cu.Write([]byte("denied"))
	if _, err := cu.Read(make([]byte, 20)); err == nil {
		t.Fatal("disabled UDP flow survived")
	}
	snap, _ = e.Snapshot("stable-owner")
	if snap.ActiveSessions != 0 || snap.Usage.BilledBytes != 8192 {
		t.Fatalf("disabled traffic changed accounting: %+v", snap)
	}
}
