package mieru

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

func TestOfficialUDPTransportLossExpiresWithoutAuthenticatedTraffic(t *testing.T) {
	for _, noise := range []bool{false, true} {
		name := "quiet"
		if noise {
			name = "invalid-packets"
		}
		t.Run(name, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, user := mieruUser(t, db, ledger, controller, 2000)
			_, other := mieruUser(t, db, ledger, controller, 1000)
			server := startNative(t, controller, "udp", user, other)
			client := officialClient(t, server.Addresses()[0], user)
			if err := client.Stop(); err != nil {
				t.Fatal(err)
			}
			config, err := client.Load()
			if err != nil {
				t.Fatal(err)
			}
			dialer := &disappearingPacketDialer{}
			config.PacketDialer = dialer
			if err := client.Store(config); err != nil {
				t.Fatal(err)
			}
			if err := client.Start(); err != nil {
				t.Fatal(err)
			}
			targets := []net.Addr{nativeEcho(t, "tcp"), nativeEcho(t, "udp")}
			for _, target := range targets {
				conn := wireEcho(t, client, target, []byte("before-loss"))
				if err := conn.SetDeadline(time.Time{}); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { dialer.disconnect(); _ = client.Stop() })
			if stats := server.mux.ServerResourceStats(); stats.Sessions != 2 || len(server.OnlineSessions()) != 2 {
				t.Fatalf("fixture did not establish both payload associations: %+v", stats)
			}
			before, err := ledger.Read(t.Context(), user.PolicyID)
			if err != nil || before.Up != 22 || before.Down != 22 || before.Billed != 88 {
				t.Fatalf("payload accounting before loss: %+v / %v", before, err)
			}
			// Close the physical sockets first: the official client cannot deliver a session close.
			// The production 60-second idle TTL and five-second maintenance cycle are unchanged.
			lostAt := time.Now()
			if count := dialer.disconnect(); count == 0 {
				t.Fatal("fixture did not capture an official UDP socket")
			}
			if err := client.Stop(); err != nil {
				t.Fatal(err)
			}
			if noise {
				startInvalidPacketTraffic(t, server.Addresses()[0])
			}
			time.Sleep(5 * time.Second)
			if stats := server.mux.ServerResourceStats(); stats.Sessions != 2 || len(server.OnlineSessions()) != 2 {
				t.Fatalf("peer sent a close or fixture did not exercise idle expiry: %+v", stats)
			}
			deadline := lostAt.Add(67 * time.Second)
			for time.Now().Before(deadline) {
				if server.mux.ServerResourceStats().Sessions == 0 && len(server.OnlineSessions()) == 0 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			stats, online := server.mux.ServerResourceStats(), server.OnlineSessions()
			if stats.Sessions != 0 || stats.BufferedBytes != 0 || len(online) != 0 {
				t.Fatalf("silent UDP peer retained resources after %s: native=%+v online=%+v", time.Since(lostAt), stats, online)
			}
			t.Logf("real UDP transport loss reclaimed TCP and UDP payload sessions in %s (bound 67s)", time.Since(lostAt))
			fresh, err := controller.Open(t.Context(), user.PolicyID)
			if err != nil {
				t.Fatal(err)
			}
			fresh.Close()
			healthy := officialClient(t, server.Addresses()[0], other)
			for _, target := range targets {
				wireEcho(t, healthy, target, []byte("listener-survived"))
			}
			after, err := ledger.Read(t.Context(), user.PolicyID)
			if err != nil || before.Up != after.Up || before.Down != after.Down || before.Billed != after.Billed {
				t.Fatalf("transport loss changed settled payload usage: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}

type disappearingPacketDialer struct {
	mu           sync.Mutex
	connections  []net.PacketConn
	disconnected bool
}

func (d *disappearingPacketDialer) ListenPacket(ctx context.Context, network, laddr, _ string) (net.PacketConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disconnected {
		return nil, net.ErrClosed
	}
	conn, err := (&net.ListenConfig{}).ListenPacket(ctx, network, laddr)
	if err == nil {
		d.connections = append(d.connections, conn)
	}
	return conn, err
}

func (d *disappearingPacketDialer) disconnect() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.disconnected = true
	for _, conn := range d.connections {
		_ = conn.Close()
	}
	return len(d.connections)
}

func startInvalidPacketTraffic(t *testing.T, target net.Addr) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = conn.WriteTo([]byte{0}, target)
			}
		}
	}()
	t.Cleanup(func() { cancel(); _ = conn.Close(); <-done })
}
