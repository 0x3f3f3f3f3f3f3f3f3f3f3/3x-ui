package pipe_test

import (
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestManagedEmptyDatagramsConsumeQueueCapacityAndSurviveClose(t *testing.T) {
	reader, writer := pipe.New(pipe.WithSizeLimit(0))
	defer reader.Interrupt()
	defer writer.Close()
	dest := net.UDPDestination(net.LocalHostIP, 12345)
	packet := func() buf.MultiBuffer {
		b := buf.New()
		b.UDP = &dest
		return buf.MultiBuffer{b}
	}
	if err := writer.WriteMultiBuffer(packet()); err != nil {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() { second <- writer.WriteMultiBuffer(packet()) }()
	select {
	case err := <-second:
		t.Fatalf("second empty datagram bypassed the full queue: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	mb, err := reader.ReadMultiBufferTimeout(time.Second)
	if err != nil || len(mb) != 1 || mb.Len() != 0 || mb[0].UDP == nil || *mb[0].UDP != dest {
		buf.ReleaseMulti(mb)
		t.Fatalf("first empty datagram or its peer was lost: %v", err)
	}
	buf.ReleaseMulti(mb)
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reading a packet did not release queue capacity")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	mb, err = reader.ReadMultiBufferTimeout(time.Second)
	defer buf.ReleaseMulti(mb)
	if err != nil || len(mb) != 1 || mb.Len() != 0 || mb[0].UDP == nil || *mb[0].UDP != dest {
		t.Fatalf("close discarded the already accepted empty datagram: %v", err)
	}
}
