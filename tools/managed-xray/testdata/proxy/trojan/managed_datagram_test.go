package trojan_test

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/proxy/trojan"
)

func TestManagedTrojanKeepsMaximumAddressAndPayloadInOneFrame(t *testing.T) {
	domain := strings.Repeat("a", 255)
	var wire bytes.Buffer
	writer := &trojan.PacketWriter{Writer: &wire, Target: net.UDPDestination(net.DomainAddress(domain), 12345)}
	payload := bytes.Repeat([]byte{19}, 7932)
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(payload)}); err != nil {
		t.Fatal(err)
	}
	frame := wire.Bytes()
	if len(frame) != 8195 || frame[0] != 3 || frame[1] != 255 || string(frame[2:257]) != domain || binary.BigEndian.Uint16(frame[257:259]) != 12345 || binary.BigEndian.Uint16(frame[259:261]) != 7932 || !bytes.Equal(frame[261:263], []byte{'\r', '\n'}) || !bytes.Equal(frame[263:], payload) {
		t.Fatal("packet writer omitted or truncated maximum-width address framing")
	}
}

func TestManagedTrojanRejectsInvalidDatagramDelimiter(t *testing.T) {
	wire := []byte{1, 127, 0, 0, 1, 0x30, 0x39, 0, 1, 'X', '\n', 19}
	reader := &trojan.PacketReader{Reader: bytes.NewReader(wire)}
	mb, err := reader.ReadMultiBuffer()
	defer buf.ReleaseMulti(mb)
	if err == nil || !strings.Contains(err.Error(), "invalid packet delimiter") || len(mb) != 0 {
		t.Fatalf("accepted malformed packet framing: packets=%d error=%v", len(mb), err)
	}
}

func TestManagedTrojanRejectsLengthOverflowBeforeWriting(t *testing.T) {
	var wire bytes.Buffer
	writer := &trojan.PacketWriter{Writer: &wire, Target: net.UDPDestination(net.LocalHostIP, 12345)}
	err := writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(make([]byte, 65536))})
	if err == nil || !strings.Contains(err.Error(), "oversize payload") || wire.Len() != 0 {
		t.Fatalf("length overflow reached transport: wrote=%d error=%v", wire.Len(), err)
	}
}
