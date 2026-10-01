package buf_test

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestUDPDatagramReaderKeepsCompletePackets(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	sender, err := net.DialUDP("udp", nil, listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	payloads := [][]byte{bytes.Repeat([]byte{0x41}, 13000), []byte("next-packet")}
	for _, payload := range payloads {
		if _, err := sender.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	reader := buf.NewPacketReader(listener)
	_ = listener.SetReadDeadline(time.Now().Add(time.Second))
	for _, payload := range payloads {
		mb, err := reader.ReadMultiBuffer()
		if err != nil {
			t.Fatal(err)
		}
		if len(mb) != 1 || !bytes.Equal(mb[0].Bytes(), payload) {
			length := mb.Len()
			buf.ReleaseMulti(mb)
			t.Fatalf("UDP datagram truncated, split or coalesced: %d bytes, want %d", length, len(payload))
		}
		buf.ReleaseMulti(mb)
	}
}

func TestUDPDatagramPipePreservesEmptyPayloadAndDestination(t *testing.T) {
	reader, writer := pipe.New()
	defer reader.Interrupt()
	defer writer.Close()
	target := xnet.UDPDestination(xnet.LocalHostIP, 12345)
	packet := buf.New()
	packet.UDP = &target
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{packet}); err != nil {
		t.Fatal(err)
	}
	mb, err := reader.ReadMultiBufferTimeout(150 * time.Millisecond)
	if err != nil {
		t.Fatalf("empty UDP datagram lost before reading its destination: %v", err)
	}
	defer buf.ReleaseMulti(mb)
	if len(mb) != 1 || mb[0].Len() != 0 || mb[0].UDP == nil || *mb[0].UDP != target {
		t.Fatalf("empty UDP datagram lost metadata: %+v", mb)
	}
}
