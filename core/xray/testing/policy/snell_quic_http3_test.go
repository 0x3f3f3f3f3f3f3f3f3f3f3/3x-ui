package policy_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	"github.com/xtls/xray-core/app/clientpolicy"
)

// This is an independent test wire client around real quic-go packets. It is
// distinct from, and does not claim to be, the proprietary official Surge app.
type snellQUICWirePacketConn struct {
	net.PacketConn
	proxy  net.Addr
	target net.Addr
}

func (c *snellQUICWirePacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	wire := p
	if len(p) > 5 && p[0]&0xc0 == 0xc0 {
		version := binary.BigEndian.Uint32(p[1:5])
		if version == 1 && p[0]&0x30 == 0 || version == 0x6b3343cf && p[0]&0x30 == 0x10 {
			var err error
			wire, err = snellQUICEnvelopeValue(snellPSK, c.target, p, "http3-untrusted-wire-id")
			if err != nil {
				return 0, err
			}
		}
	}
	n, err := c.PacketConn.WriteTo(wire, c.proxy)
	if n == len(wire) {
		return len(p), err
	}
	return 0, err
}

func (c *snellQUICWirePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, _, err := c.PacketConn.ReadFrom(p)
	return n, c.target, err
}

func TestNativeSnellV5QUICRealV1V2HTTP3BothDirections(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound-official"} {
		for _, version := range []quic.Version{quic.Version1, quic.Version2} {
			t.Run(fmt.Sprintf("%s/quic-%s", direction, version), func(t *testing.T) {
				tlsFixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				t.Cleanup(tlsFixture.Close)
				target, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				server := &http3.Server{TLSConfig: tlsFixture.TLS.Clone(), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(w, r.Body) })}
				serverDone := make(chan struct{})
				go func() { defer close(serverDone); _ = server.Serve(target) }()
				t.Cleanup(func() { _ = server.Close(); _ = target.Close(); <-serverDone })
				listen := port(t)
				config := snellNativeConfig(5, listen)
				var packet net.PacketConn
				if direction == "outbound-official" {
					reference := snellReferenceServer(t, 5, "", "", snellPSK)
					time.Sleep(100 * time.Millisecond)
					config["inbounds"] = []any{map[string]any{"tag": "origin", "listen": "127.0.0.1", "port": listen, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": "udp", "rewriteAddress": "127.0.0.1", "rewritePort": target.LocalAddr().(*net.UDPAddr).Port, "clientId": snellOwner}}}
					config["outbounds"] = []any{map[string]any{"protocol": "snell", "settings": map[string]any{"version": 5, "psk": snellPSK, "address": "127.0.0.1", "port": reference, "quic": true}}}
				} else {
					underlying, err := net.ListenPacket("udp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { underlying.Close() })
					packet = &snellQUICWirePacketConn{PacketConn: underlying, proxy: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: listen}, target: target.LocalAddr()}
				}
				instance := start(t, loopbackJSON(t, config))
				transport := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, QUICConfig: &quic.Config{Versions: []quic.Version{version}, HandshakeIdleTimeout: 2 * time.Second, MaxIdleTimeout: 3 * time.Second}}
				transport.Dial = func(ctx context.Context, _ string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
					if packet != nil {
						return quic.Dial(ctx, packet, target.LocalAddr(), tlsCfg, cfg)
					}
					return quic.DialAddr(ctx, fmt.Sprintf("127.0.0.1:%d", listen), tlsCfg, cfg)
				}
				t.Cleanup(func() { transport.Close() })
				payload := bytes.Repeat([]byte("native-snell-real-http3"), 4096)
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+target.LocalAddr().String()+"/", bytes.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				response, err := (&http.Client{Transport: transport}).Do(request)
				if err != nil {
					t.Fatalf("real QUIC/HTTP3 handshake through %s failed: %v", direction, err)
				}
				actual, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.ProtoMajor != 3 || !bytes.Equal(actual, payload) {
					t.Fatalf("HTTP3 full body changed: proto=%s want=%d got=%d err=%v", response.Proto, len(payload), len(actual), err)
				}
				snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
				if err != nil || snap.Usage.RawUpload < uint64(len(payload)) || snap.Usage.RawDownload < uint64(len(payload)) {
					t.Fatalf("real QUIC payload bypassed canonical ledger: %+v err=%v", snap, err)
				}
			})
		}
	}
}
