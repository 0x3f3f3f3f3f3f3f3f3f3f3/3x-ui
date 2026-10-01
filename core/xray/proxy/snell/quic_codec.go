/*
 * Pure envelope codec adapted from OpenSnell, GPL-3.0-or-later.
 * Complete original source, license and extraction inventory are retained at
 * core/deps/opensnell and core/deps/opensnell.UPSTREAM.json.
 */
package snell

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode"

	S "github.com/sagernet/sing-snell"
	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
)

const (
	quicSaltLen         = 16
	quicHeaderCipherLen = 7 + 16
	quicMaxEnvelopeSize = int(buf.MaxDatagramSize)
	quicFixedBit        = 0x40
)

func decodeQUICEnvelope(psk, packet []byte) (X.Destination, []byte, error) {
	if len(psk) == 0 || len(packet) < quicSaltLen+quicHeaderCipherLen+16 || len(packet) > quicMaxEnvelopeSize || packet[0]&quicFixedBit != 0 {
		return X.Destination{}, nil, errors.New("invalid Snell QUIC envelope")
	}
	aead, err := S.NewAEAD(S.DeriveKey(psk, packet[:quicSaltLen]))
	if err != nil {
		return X.Destination{}, nil, err
	}
	rest := packet[quicSaltLen:]
	nonce := make([]byte, aead.NonceSize())
	header, err := aead.Open(nil, nonce, rest[:quicHeaderCipherLen], nil)
	if err != nil {
		return X.Destination{}, nil, err
	}
	if len(header) != 7 || header[0] != 4 || header[1] != 0 || header[2] != 0 {
		return X.Destination{}, nil, errors.New("unsupported Snell QUIC frame")
	}
	padding := int(binary.BigEndian.Uint16(header[3:5]))
	length := int(binary.BigEndian.Uint16(header[5:7]))
	offset := quicHeaderCipherLen + padding
	if offset+length+aead.Overhead() != len(rest) {
		return X.Destination{}, nil, errors.New("invalid Snell QUIC record length")
	}
	binary.LittleEndian.PutUint64(nonce, 1)
	payload, err := aead.Open(nil, nonce, rest[offset:], nil)
	if err != nil {
		return X.Destination{}, nil, err
	}
	if len(payload) < 6 || payload[0] != 1 || payload[1] != 1 {
		return X.Destination{}, nil, errors.New("unsupported Snell QUIC request")
	}
	offset = 3 + int(payload[2]) // Wire client ID is not an independently authenticated identity.
	if offset >= len(payload) {
		return X.Destination{}, nil, errors.New("invalid Snell QUIC client ID length")
	}
	hostLen := int(payload[offset])
	offset++
	if hostLen == 0 || offset+hostLen+2 >= len(payload) {
		return X.Destination{}, nil, errors.New("invalid Snell QUIC target length")
	}
	host := string(payload[offset : offset+hostLen])
	if invalidQUICHost(host) {
		return X.Destination{}, nil, errors.New("invalid Snell QUIC target host")
	}
	port := X.Port(binary.BigEndian.Uint16(payload[offset+hostLen : offset+hostLen+2]))
	target := X.UDPDestination(X.ParseAddress(host), port)
	if !validQUICTarget(target) {
		return X.Destination{}, nil, errors.New("invalid Snell QUIC target")
	}
	return target, payload[offset+hostLen+2:], nil
}

func validQUICTarget(target X.Destination) bool {
	if !target.IsValid() || target.Network != X.Network_UDP || target.Port == 0 {
		return false
	}
	host := quicTargetHost(target)
	return host != "" && len(host) <= 255 && !invalidQUICHost(host)
}

func invalidQUICHost(host string) bool {
	return strings.IndexFunc(host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0
}

func quicTargetHost(target X.Destination) string {
	if target.Address.Family().IsIP() {
		// Xray's IPv6 Address.String includes brackets. The Snell envelope host
		// is a literal, not host:port; official v5 treats brackets as a bad domain.
		return target.Address.IP().String()
	}
	return target.Address.Domain()
}

func encodeQUICEnvelope(psk []byte, target X.Destination, inner []byte) ([]byte, error) {
	if len(psk) == 0 || !validQUICTarget(target) || len(inner) == 0 {
		return nil, errors.New("invalid Snell QUIC request")
	}
	host := quicTargetHost(target)
	length := 6 + len(host) + len(inner)
	if length > 0xffff || quicSaltLen+quicHeaderCipherLen+length+16 > quicMaxEnvelopeSize {
		return nil, errors.New("Snell QUIC envelope exceeds UDP wire size")
	}
	payload := make([]byte, 0, length)
	payload = append(payload, 1, 1, 0, byte(len(host)))
	payload = append(payload, host...)
	payload = binary.BigEndian.AppendUint16(payload, uint16(target.Port))
	payload = append(payload, inner...)
	salt := make([]byte, quicSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	// Official v5 treats fixed-bit-set packets as raw QUIC. Four-prefix fixture
	// probes verify that clearing only this bit admits both other prefix classes.
	salt[0] &^= quicFixedBit
	aead, err := S.NewAEAD(S.DeriveKey(psk, salt))
	if err != nil {
		return nil, err
	}
	header := make([]byte, 7)
	header[0] = 4
	binary.BigEndian.PutUint16(header[5:7], uint16(length))
	nonce := make([]byte, aead.NonceSize())
	packet := append(salt, aead.Seal(nil, nonce, header, nil)...)
	binary.LittleEndian.PutUint64(nonce, 1)
	return append(packet, aead.Seal(nil, nonce, payload, nil)...), nil
}
