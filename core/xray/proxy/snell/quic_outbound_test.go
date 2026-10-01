package snell

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
)

func TestNativeSnellQUICInitialSelectionUsesPublicV1V2Header(t *testing.T) {
	_, initial, err := decodeQUICEnvelope([]byte("JFdZLmZLtYtUP308QrqdoA=="), realCapturedEnvelopePkt1)
	if err != nil || !isQUICInitial(initial) {
		t.Fatalf("captured official Initial was not selected: %v", err)
	}
	v2 := append([]byte(nil), initial...)
	v2[0] = v2[0]&^0x30 | 0x10
	binary.BigEndian.PutUint32(v2[1:5], 0x6b3343cf)
	if !isQUICInitial(v2) {
		t.Fatal("public QUIC v2 Initial header was not selected")
	}
	for _, packet := range [][]byte{nil, initial[:1199], bytes.Repeat([]byte{0x71}, 1300), append([]byte{0x40}, initial[1:]...)} {
		if isQUICInitial(packet) {
			t.Fatal("ordinary or short UDP incorrectly selected native QUIC")
		}
	}
	for _, mutation := range []func([]byte){func(b []byte) { b[0] |= 0x30 }, func(b []byte) { b[4] = 7 }, func(b []byte) { b[5] = 21 }, func(b []byte) { b[5] = 255 }} {
		packet := append([]byte(nil), initial...)
		mutation(packet)
		if isQUICInitial(packet) {
			t.Fatal("invalid long-header structure selected native QUIC")
		}
	}
}

type quicLateDialer struct {
	lateDialer
	destination X.Destination
}

func (d *quicLateDialer) Dial(ctx context.Context, destination X.Destination) (stat.Connection, error) {
	d.destination = destination
	return d.lateDialer.Dial(ctx, destination)
}

func TestNativeSnellQUICLateDialFencesHandlerCredentialAndRequest(t *testing.T) {
	for _, mode := range []string{"handler", "credential", "request"} {
		t.Run(mode, func(t *testing.T) {
			out, err := NewClient(context.Background(), &ClientConfig{Version: 5, Psk: "native-quic-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 7177})
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			user := &protocol.MemoryUser{ClientID: "owner", Email: "quic-owner"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = session.ContextWithInbound(ctx, &session.Inbound{User: user, Tag: "credential-generation"})
			ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.UDPDestination(X.LocalHostIP, 443)}})
			raw, peer := net.Pipe()
			defer peer.Close()
			d := &quicLateDialer{lateDialer: lateDialer{entered: make(chan struct{}), release: make(chan struct{}), conn: raw}}
			_, initial, err := decodeQUICEnvelope([]byte("JFdZLmZLtYtUP308QrqdoA=="), realCapturedEnvelopePkt1)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- out.Process(ctx, &transport.Link{Reader: &eofReader{payload: initial}, Writer: new(bytesWriter)}, d)
			}()
			<-d.entered
			if d.destination.Network != X.Network_UDP || d.destination.Port != 7177 {
				t.Fatalf("native QUIC bypassed supplied server UDP dial: %v", d.destination)
			}
			switch mode {
			case "handler":
				out.Close()
			case "credential":
				user.RevokeCredential()
			case "request":
				cancel()
			}
			close(d.release)
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("late QUIC dial entered revoked scope")
				}
			case <-time.After(time.Second):
				t.Fatal("late QUIC dial/copy goroutines did not join")
			}
			peer.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("late raw QUIC transport stayed open: %v", err)
			}
			out.mu.Lock()
			active, pending := len(out.connections), len(out.pending)
			out.mu.Unlock()
			if active != 0 || pending != 0 {
				t.Fatalf("late QUIC retained physical resources: active=%d pending=%d", active, pending)
			}
		})
	}
}

type quicHandshakePolicy struct{ timeoutPolicy }

func (quicHandshakePolicy) ForLevel(uint32) policy.Session {
	return policy.Session{Timeouts: policy.Timeout{Handshake: 100 * time.Millisecond, ConnectionIdle: 5 * time.Second, UplinkOnly: 5 * time.Second, DownlinkOnly: 5 * time.Second}}
}

func TestNativeSnellQUICFirstWriteObeysHandshakeBound(t *testing.T) {
	out, err := NewClient(context.Background(), &ClientConfig{Version: 5, Psk: "native-quic-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	instance := new(core.Instance)
	if err = instance.AddFeature(quicHandshakePolicy{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.UDPDestination(X.LocalHostIP, 443)}})
	raw, peer := net.Pipe()
	defer peer.Close()
	release := make(chan struct{})
	close(release)
	dialer := &lateDialer{entered: make(chan struct{}), release: release, conn: raw}
	_, initial, err := decodeQUICEnvelope([]byte("JFdZLmZLtYtUP308QrqdoA=="), realCapturedEnvelopePkt1)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- out.Process(ctx, &transport.Link{Reader: &eofReader{payload: initial}, Writer: new(bytesWriter)}, dialer)
	}()
	<-dialer.entered
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked initial QUIC envelope unexpectedly succeeded")
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		out.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("could not join blocked QUIC probe")
		}
		t.Fatal("initial QUIC write exceeded 100ms handshake bound")
	}
}
