package policy_test

import (
	"bytes"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	coreSnell "github.com/xtls/xray-core/proxy/snell"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type quicReferenceDialer struct{ udp, tcp atomic.Int32 }

func (d *quicReferenceDialer) Dial(ctx context.Context, destination X.Destination) (stat.Connection, error) {
	if destination.Network == X.Network_UDP {
		d.udp.Add(1)
	} else {
		d.tcp.Add(1)
	}
	return (&net.Dialer{}).DialContext(ctx, destination.Network.SystemString(), destination.NetAddr())
}
func (*quicReferenceDialer) DestIpAddress() X.IP                                   { return nil }
func (*quicReferenceDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func TestNativeSnellV5QUICOfficialDirectFirst13kDistinctTargetsAndOrdinaryUDP(t *testing.T) {
	server := snellReferenceServer(t, 5, "", "", snellPSK)
	time.Sleep(100 * time.Millisecond)
	first, firstBytes := snellPacketEcho(t, "127.0.0.1")
	second, secondBytes := snellPacketEcho(t, "::1")
	ordinary, ordinaryBytes := snellPacketEcho(t, "127.0.0.1")
	out, err := coreSnell.NewClient(context.Background(), &coreSnell.ClientConfig{Version: 5, Psk: snellPSK, Address: X.NewIPOrDomain(X.LocalHostIP), Port: uint32(server)})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstTarget := X.DestinationFromAddr(first.LocalAddr())
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: firstTarget}})
	reader := &referencePacketReader{ctx: ctx, cancel: cancel, packets: make(chan buf.MultiBuffer, 1)}
	writer := &referencePacketWriter{ctx: ctx, cancel: cancel, packets: make(chan buf.MultiBuffer, 8)}
	dialer := new(quicReferenceDialer)
	finished := make(chan error, 1)
	go func() { finished <- out.Process(ctx, &transport.Link{Reader: reader, Writer: writer}, dialer) }()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("official QUIC bridge did not join its copy goroutines")
		}
		for len(writer.packets) > 0 {
			buf.ReleaseMulti(<-writer.packets)
		}
		for len(reader.packets) > 0 {
			buf.ReleaseMulti(<-reader.packets)
		}
	}()
	initial := snellQUICInitial(13000)
	for _, packet := range []struct {
		target  net.Addr
		payload []byte
	}{{first.LocalAddr(), initial}, {second.LocalAddr(), initial}, {first.LocalAddr(), []byte{0x40, 0x71}}, {second.LocalAddr(), []byte{0x40, 0x72}}, {ordinary.LocalAddr(), []byte("ordinary-datagram")}} {
		target := X.DestinationFromAddr(packet.target)
		b := buf.NewWithSize(int32(len(packet.payload)))
		b.Write(packet.payload)
		b.UDP = &target
		reader.packets <- buf.MultiBuffer{b}
		select {
		case response := <-writer.packets:
			good := len(response) == 1 && bytes.Equal(response[0].Bytes(), packet.payload) && response[0].UDP != nil && *response[0].UDP == target
			length := response.Len()
			buf.ReleaseMulti(response)
			if !good {
				t.Fatalf("official direct first13k/target association changed: target=%v want=%d got=%d", target, len(packet.payload), length)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("official direct datagram returned no complete reply: target=%v size=%d", target, len(packet.payload))
		}
	}
	if firstBytes.Load() != 13002 || secondBytes.Load() != 13002 || ordinaryBytes.Load() != 17 || dialer.udp.Load() != 2 || dialer.tcp.Load() != 1 {
		t.Fatalf("targets shared QUIC sockets or ordinary UDP changed transport: first=%d second=%d ordinary=%d UDPdials=%d TCPdials=%d", firstBytes.Load(), secondBytes.Load(), ordinaryBytes.Load(), dialer.udp.Load(), dialer.tcp.Load())
	}
}
