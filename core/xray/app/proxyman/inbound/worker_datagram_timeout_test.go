package inbound

import (
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/signal/done"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/snell"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestUDPWorkerNativeTimeoutOwnershipPreservesLegacyReaper(t *testing.T) {
	for _, version := range []uint32{4, 5, 6} {
		t.Run(map[uint32]string{4: "v4-default", 5: "v5-owned", 6: "v6-default"}[version], func(t *testing.T) {
			native, err := snell.NewServer(context.Background(), &snell.ServerConfig{Version: version, User: &protocol.User{ClientId: "d395b8b1-31ab-47ea-9a67-e46dcc84cd96", Email: "timeout-owner", Account: serial.ToTypedMessage(&snell.Account{Psk: "native-timeout-test-secret"})}})
			if err != nil {
				t.Fatal(err)
			}
			defer native.Close()
			r, writer := pipe.New()
			defer r.Interrupt()
			defer writer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn := &udpConn{lastActivityTime: time.Now().Add(-121 * time.Second).Unix(), reader: r, writer: writer, done: done.New(), ctx: ctx, cancel: cancel}
			defer conn.Close()
			id := connID{src: X.UDPDestination(X.LocalHostIP, 30000)}
			w := &udpWorker{proxy: proxy.Inbound(native), activeConn: map[connID]*udpConn{id: conn}}
			if err := w.clean(); err != nil {
				t.Fatal(err)
			}
			if version == 5 {
				if conn.done.Done() || len(w.activeConn) != 1 {
					t.Fatal("generic120s reaper closed a source whose native adapter owns its policy timeout")
				}
				p := buf.FromBytes([]byte("still-alive"))
				p.UDP = &id.src
				if err := writer.WriteMultiBuffer(buf.MultiBuffer{p}); err != nil {
					t.Fatal(err)
				}
				mb, err := conn.ReadMultiBuffer()
				valid := len(mb) == 1 && string(mb[0].Bytes()) == "still-alive"
				buf.ReleaseMulti(mb)
				if err != nil || !valid {
					t.Fatalf("owned datagram association stopped accepting complete packets: %v", err)
				}
			} else if !conn.done.Done() || len(w.activeConn) != 0 {
				t.Fatal("default UDP worker no longer reaps legacy idle connections")
			}
		})
	}
}
