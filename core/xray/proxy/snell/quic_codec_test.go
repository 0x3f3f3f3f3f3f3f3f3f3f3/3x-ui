package snell

import (
	"bytes"
	"encoding/binary"
	"testing"

	S "github.com/sagernet/sing-snell"
	X "github.com/xtls/xray-core/common/net"
)

func TestNativeSnellQUICCapturedOfficialInitialAndAuthentication(t *testing.T) {
	psk := []byte("JFdZLmZLtYtUP308QrqdoA==")
	target, inner, err := decodeQUICEnvelope(psk, realCapturedEnvelopePkt1)
	if err != nil || target != X.UDPDestination(X.DomainAddress("www.cloudflare.com"), 443) || len(inner) != 1280 || inner[0]&0xf0 != 0xc0 {
		t.Fatalf("official capture decode: target=%v length=%d err=%v", target, len(inner), err)
	}
	for _, packet := range [][]byte{realCapturedEnvelopePkt1[:15], realCapturedEnvelopePkt1[:38], realCapturedEnvelopePkt1[:len(realCapturedEnvelopePkt1)-1]} {
		if _, _, err := decodeQUICEnvelope(psk, packet); err == nil {
			t.Fatal("truncated envelope authenticated")
		}
	}
	for _, offset := range []int{16, 39, len(realCapturedEnvelopePkt1) - 1} {
		packet := append([]byte(nil), realCapturedEnvelopePkt1...)
		packet[offset] ^= 1
		if _, _, err := decodeQUICEnvelope(psk, packet); err == nil {
			t.Fatal("corrupted envelope authenticated")
		}
	}
	if _, _, err := decodeQUICEnvelope([]byte("wrong-quic-test-secret"), realCapturedEnvelopePkt1); err == nil {
		t.Fatal("wrong PSK authenticated")
	}
}

func TestNativeSnellQUICAuthenticatedMalformedHostsAndHeaderReject(t *testing.T) {
	psk := []byte("native-quic-test-secret")
	seal := func(header, payload []byte) []byte {
		salt := bytes.Repeat([]byte{0x11}, quicSaltLen)
		aead, err := S.NewAEAD(S.DeriveKey(psk, salt))
		if err != nil {
			t.Fatal(err)
		}
		binary.BigEndian.PutUint16(header[5:7], uint16(len(payload)))
		nonce := make([]byte, aead.NonceSize())
		packet := append(salt, aead.Seal(nil, nonce, header, nil)...)
		binary.LittleEndian.PutUint64(nonce, 1)
		return append(packet, aead.Seal(nil, nonce, payload, nil)...)
	}
	for _, host := range []string{"\nexample.test", "\t127.0.0.1", "\n[::1]", "example.test\r", "example\x00.test"} {
		payload := []byte{1, 1, 0, byte(len(host))}
		payload = append(payload, host...)
		payload = append(payload, 1, 0xbb, 0xc0)
		packet := seal([]byte{4, 0, 0, 0, 0, 0, 0}, payload)
		if _, _, err := decodeQUICEnvelope(psk, packet); err == nil {
			t.Fatalf("authenticated malformed host was silently normalized: %q", host)
		}
	}
	payload := []byte{1, 1, 0, 1, 'a', 1, 0xbb, 0xc0}
	for _, header := range [][]byte{{5, 0, 0, 0, 0, 0, 0}, {4, 1, 0, 0, 0, 0, 0}, {4, 0, 0, 0xff, 0xff, 0, 0}} {
		if _, _, err := decodeQUICEnvelope(psk, seal(header, payload)); err == nil {
			t.Fatal("authenticated unsupported frame or malformed padding was accepted")
		}
	}
}

func TestNativeSnellQUICEnvelopeTargetsAndFixedBit(t *testing.T) {
	psk := []byte("native-quic-test-secret")
	for _, target := range []X.Destination{X.UDPDestination(X.DomainAddress("snell-quic.test"), 443), X.UDPDestination(X.LocalHostIP, 443), X.UDPDestination(X.IPAddress([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}), 443)} {
		for range 64 {
			inner := append([]byte{0xc0, 0, 0, 0, 1}, bytes.Repeat([]byte{0x71}, 1275)...)
			packet, err := encodeQUICEnvelope(psk, target, inner)
			if err != nil {
				t.Fatal(err)
			}
			if packet[0]&0x40 != 0 {
				t.Fatal("envelope collides with official raw QUIC fixed-bit classifier")
			}
			got, payload, err := decodeQUICEnvelope(psk, packet)
			if err != nil || got != target || !bytes.Equal(inner, payload) {
				t.Fatalf("target/payload changed: target=%v want=%v err=%v", got, target, err)
			}
		}
	}
	for _, target := range []X.Destination{X.TCPDestination(X.LocalHostIP, 443), X.UDPDestination(X.LocalHostIP, 0), X.UDPDestination(X.DomainAddress(""), 443)} {
		if _, err := encodeQUICEnvelope(psk, target, []byte{0xc0}); err == nil {
			t.Fatal("invalid QUIC target accepted")
		}
	}
	target := X.UDPDestination(X.LocalHostIP, 443)
	if _, err := encodeQUICEnvelope(psk, target, nil); err == nil {
		t.Fatal("missing QUIC Initial accepted")
	}
	if _, err := encodeQUICEnvelope(psk, target, make([]byte, 65535)); err == nil {
		t.Fatal("oversized envelope accepted")
	}
}
