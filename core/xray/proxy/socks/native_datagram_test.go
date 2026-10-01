package socks

import (
	"bytes"
	"testing"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
)

func TestNativeSnellDownloadedSOCKSDatagramsPreserveLegacyDefault(t *testing.T) {
	for _, address := range []net.Address{net.LocalHostIP, net.IPAddress([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}), net.DomainAddress("native.example")} {
		request := &protocol.RequestHeader{Address: address, Port: 8443}
		for _, size := range []int{0, 13000} {
			payload := bytes.Repeat([]byte{0x71}, size)
			packet, err := encodeUDPPacket(request, payload, true)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeUDPPacket(packet)
			if err != nil || decoded.Address.String() != request.Address.String() || decoded.Port != request.Port || !bytes.Equal(packet.Bytes(), payload) {
				packet.Release()
				t.Fatalf("explicit native SOCKS datagram lost boundary, address or bytes: size=%d err=%v", size, err)
			}
			packet.Release()
			legacy, err := EncodeUDPPacket(request, payload)
			if err != nil {
				t.Fatal(err)
			}
			if size == 13000 && !legacy.IsEmpty() {
				legacy.Release()
				t.Fatal("legacy SOCKS size boundary changed without opt-in")
			}
			legacy.Release()
		}
	}
}
