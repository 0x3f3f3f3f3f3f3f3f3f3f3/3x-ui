//go:build !wasm && !openbsd

package buf_test

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/net/cnc"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestTrackedConnectionPreservesRawReadv(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	tracked := stat.NewCounterConnection(server, nil, nil, func() {})
	reader := buf.NewReader(tracked)
	if _, ok := reader.(*buf.ReadVReader); !ok {
		t.Fatalf("tracking disabled raw readv: %T", reader)
	}
	if _, err := client.Write([]byte("readv preserved")); err != nil {
		t.Fatal(err)
	}
	mb, err := reader.ReadMultiBuffer()
	defer buf.ReleaseMulti(mb)
	if err != nil || mb.Len() != 15 || string(mb[0].Bytes()) != "readv preserved" {
		t.Fatalf("tracked readv transfer = %v %v", mb, err)
	}
}

func TestTrackedConnectionPreservesMultiBufferDatagrams(t *testing.T) {
	for _, direction := range []string{"read", "write"} {
		t.Run(direction, func(t *testing.T) {
			reader, writer := pipe.New()
			defer reader.Interrupt()
			defer writer.Close()
			raw := cnc.NewConnection(cnc.ConnectionInputMulti(writer), cnc.ConnectionOutputMulti(reader))
			tracked := stat.NewCounterConnection(raw, nil, nil, func() {})
			input := buf.New()
			_, _ = input.Write([]byte("packet"))
			destination := xnet.UDPDestination(xnet.LocalHostIP, 43210)
			input.UDP = &destination
			var mb buf.MultiBuffer
			var err error
			done := make(chan error, 1)
			if direction == "write" {
				go func() { done <- buf.NewWriter(tracked).WriteMultiBuffer(buf.MultiBuffer{input}) }()
				mb, err = reader.ReadMultiBuffer()
			} else {
				go func() { done <- writer.WriteMultiBuffer(buf.MultiBuffer{input}) }()
				mb, err = buf.NewReader(tracked).ReadMultiBuffer()
			}
			defer buf.ReleaseMulti(mb)
			if writeErr := <-done; writeErr != nil {
				t.Fatal(writeErr)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(mb) != 1 || string(mb[0].Bytes()) != "packet" {
				t.Fatal(io.ErrUnexpectedEOF)
			}
			if mb[0].UDP == nil || *mb[0].UDP != destination {
				t.Fatalf("tracking discarded %s datagram destination: %v", direction, mb[0].UDP)
			}
		})
	}
}
