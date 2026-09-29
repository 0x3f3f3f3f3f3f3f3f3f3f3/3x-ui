package buf_test

import (
	"bytes"
	"io"
	"net"
	"testing"
	"testing/iotest"
	"time"

	"github.com/xtls/xray-core/common/buf"
)

func TestManagedPacketReaderKeepsEmptyAndLargeDatagrams(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := buf.NewPacketReader(conn)
	for _, size := range []int{0, 8193, 65507} {
		payload := bytes.Repeat([]byte{17}, size)
		if _, err := listener.WriteToUDP(payload, conn.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		mb, err := reader.ReadMultiBuffer()
		if err != nil {
			t.Fatalf("lost %d-byte datagram: %v", size, err)
		}
		if len(mb) != 1 || mb.IsEmpty() || mb.Len() != int32(size) || !bytes.Equal(mb[0].Bytes(), payload) {
			buf.ReleaseMulti(mb)
			t.Fatalf("changed %d-byte datagram or treated it as absent", size)
		}
		buf.ReleaseMulti(mb)
	}
}

func TestManagedPacketReaderRetainsFinalPayloadWithEOF(t *testing.T) {
	reader := buf.NewPacketReader(iotest.DataErrReader(bytes.NewReader([]byte("final packet"))))
	mb, err := reader.ReadMultiBuffer()
	if err != nil || len(mb) != 1 || !bytes.Equal(mb[0].Bytes(), []byte("final packet")) {
		buf.ReleaseMulti(mb)
		t.Fatalf("discarded valid terminal packet: %v", err)
	}
	buf.ReleaseMulti(mb)
	mb, err = reader.ReadMultiBuffer()
	defer buf.ReleaseMulti(mb)
	if err != io.EOF || len(mb) != 0 {
		t.Fatalf("terminal read became an invented empty datagram: %d, %v", len(mb), err)
	}
}
