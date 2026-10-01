package socks_test

import (
	"bytes"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy/socks"
)

// SOCKS headers occupy UDP payload bytes too. A valid maximum packet must
// survive both adapters; an oversize packet must not become an empty write.
func TestUDPWriterRespectsWireHeaderLimit(t *testing.T) {
	for _, tc := range []struct {
		name, address string
		maximum       int
	}{
		{"ipv4", "127.0.0.1", 65497},
		{"ipv6", "::1", 65485},
		{"domain", "localhost", 65491},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &protocol.RequestHeader{Address: net.ParseAddress(tc.address), Port: 1234}
			var wire bytes.Buffer
			writer := &socks.UDPWriter{Writer: &wire, Request: request}
			payload := bytes.Repeat([]byte{0x87}, tc.maximum)
			if err := writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(payload)}); err != nil {
				t.Fatal(err)
			}
			if wire.Len() != 65507 {
				t.Fatalf("encoded wire payload: got=%d want=65507", wire.Len())
			}
			reader := &socks.UDPReader{Reader: &wire}
			decoded, err := reader.ReadMultiBuffer()
			if err != nil {
				t.Fatal(err)
			}
			defer buf.ReleaseMulti(decoded)
			if len(decoded) != 1 || !bytes.Equal(decoded[0].Bytes(), payload) || decoded[0].UDP.Address.String() != request.Address.String() || decoded[0].UDP.Port != request.Port {
				t.Fatal("maximum wire datagram lost payload or destination")
			}
			wire.Reset()
			if err := writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(append(payload, 1))}); err == nil {
				t.Fatal("oversize SOCKS wire payload was silently accepted")
			}
			if wire.Len() != 0 {
				t.Fatal("oversize payload wrote a truncated or empty datagram")
			}
		})
	}
}
