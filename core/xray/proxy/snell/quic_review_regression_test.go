package snell

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	S "github.com/sagernet/sing-snell"
	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type reviewWaitingReader struct {
	ctx     context.Context
	cancel  context.CancelFunc
	entered chan struct{}
	once    sync.Once
}

func (r *reviewWaitingReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.ctx.Done()
	return nil, r.ctx.Err()
}
func (r *reviewWaitingReader) Interrupt() { r.cancel() }

func TestNativeSnellQUICCloseBeforeFirstDatagram(t *testing.T) {
	for _, mode := range []string{"handler", "credential"} {
		t.Run(mode, func(t *testing.T) {
			out, err := NewClient(context.Background(), &ClientConfig{Version: 5, Psk: "native-quic-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 7177})
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			u := &protocol.MemoryUser{ClientID: "owner", Email: "credential-generation"}
			ctx = session.ContextWithInbound(ctx, &session.Inbound{User: u})
			ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.UDPDestination(X.LocalHostIP, 443)}})
			reader := &reviewWaitingReader{ctx: ctx, cancel: cancel, entered: make(chan struct{})}
			done := make(chan error, 1)
			go func() {
				done <- out.Process(ctx, &transport.Link{Reader: reader, Writer: new(bytesWriter)}, new(countDialer))
			}()
			<-reader.entered
			if mode == "handler" {
				out.Close()
			} else {
				u.RevokeCredential()
			}
			select {
			case <-done:
			case <-time.After(200 * time.Millisecond):
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("cleanup could not join")
				}
				t.Fatal("close/revoke left the v5 packet request and link waiting before its first datagram")
			}
		})
	}
}

type reviewBlockingDialer struct{ entered chan struct{} }

func (d *reviewBlockingDialer) Dial(ctx context.Context, _ X.Destination) (stat.Connection, error) {
	d.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*reviewBlockingDialer) DestIpAddress() X.IP                                   { return nil }
func (*reviewBlockingDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func TestNativeSnellQUICConcurrentPendingBoundAndJoin(t *testing.T) {
	out, err := NewClient(context.Background(), &ClientConfig{Version: 5, Psk: "native-quic-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 7177})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	_, initial, err := decodeQUICEnvelope([]byte("JFdZLmZLtYtUP308QrqdoA=="), realCapturedEnvelopePkt1)
	if err != nil {
		t.Fatal(err)
	}
	d := &reviewBlockingDialer{entered: make(chan struct{}, 128)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.UDPDestination(X.LocalHostIP, 443)}})
	done := make(chan error, 128)
	for range 128 {
		go func() {
			requestCtx := session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.UDPDestination(X.LocalHostIP, 443)}})
			done <- out.Process(requestCtx, &transport.Link{Reader: &eofReader{payload: initial}, Writer: new(bytesWriter)}, d)
		}()
	}
	for range 128 {
		select {
		case <-d.entered:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("available pending slots not admitted")
		}
	}
	out.mu.Lock()
	active, pending := len(out.connections), len(out.pending)
	out.mu.Unlock()
	if active != 0 || pending != 128 {
		t.Fatalf("pending capacity mismatch: %d,%d", active, pending)
	}
	overflow := out.Process(ctx, &transport.Link{Reader: &eofReader{payload: initial}, Writer: new(bytesWriter)}, d)
	if overflow == nil || errors.Is(overflow, context.Canceled) {
		t.Fatalf("expected capacity rejection, got %v", overflow)
	}
	out.Close()
	for range 128 {
		select {
		case <-done:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("handler close failed to join pending packet requests")
		}
	}
	out.mu.Lock()
	active, pending = len(out.connections), len(out.pending)
	out.mu.Unlock()
	if active != 0 || pending != 0 {
		t.Fatalf("closed resources retained: %d,%d", active, pending)
	}
}

type reviewHandshakePhasePolicy struct{ timeoutPolicy }

func (reviewHandshakePhasePolicy) ForLevel(uint32) policy.Session {
	return policy.Session{Timeouts: policy.Timeout{Handshake: 500 * time.Millisecond, ConnectionIdle: 50 * time.Millisecond, UplinkOnly: time.Second, DownlinkOnly: time.Second}}
}

type reviewSlowAllowedDialer struct {
	entered  chan struct{}
	canceled chan time.Duration
	started  time.Time
}

func (d *reviewSlowAllowedDialer) Dial(ctx context.Context, _ X.Destination) (stat.Connection, error) {
	close(d.entered)
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil, errors.New("review allowed dial interval elapsed")
	case <-ctx.Done():
		d.canceled <- time.Since(d.started)
		return nil, ctx.Err()
	}
}

func (*reviewSlowAllowedDialer) DestIpAddress() X.IP                                   { return nil }
func (*reviewSlowAllowedDialer) SetOutboundGateway(context.Context, *session.Outbound) {}
func TestNativeSnellQUICHandshakePrecedesConnectionIdle(t *testing.T) {
	out, err := NewClient(context.Background(), &ClientConfig{Version: 5, Psk: "native-quic-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 7177})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	instance := new(core.Instance)
	if err = instance.AddFeature(reviewHandshakePhasePolicy{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: X.UDPDestination(X.LocalHostIP, 443)}})
	_, initial, err := decodeQUICEnvelope([]byte("JFdZLmZLtYtUP308QrqdoA=="), realCapturedEnvelopePkt1)
	if err != nil {
		t.Fatal(err)
	}
	d := &reviewSlowAllowedDialer{entered: make(chan struct{}), canceled: make(chan time.Duration, 1), started: time.Now()}
	result := out.Process(ctx, &transport.Link{Reader: &eofReader{payload: initial}, Writer: new(bytesWriter)}, d)
	select {
	case elapsed := <-d.canceled:
		t.Fatalf("ConnectionIdle canceled initial dial at %s before 500ms Handshake allowance: %v", elapsed, result)
	default:
	}
}

func TestNativeSnellQUICRejectsEmbeddedHostWhitespace(t *testing.T) {
	psk := []byte("native-quic-test-secret")
	host := "[\t127.0.0.1\t]"
	payload := []byte{1, 1, 0, byte(len(host))}
	payload = append(payload, host...)
	payload = append(payload, 1, 0xbb, 0xc0)
	salt := bytes.Repeat([]byte{0x11}, quicSaltLen)
	aead, err := S.NewAEAD(S.DeriveKey(psk, salt))
	if err != nil {
		t.Fatal(err)
	}
	header := []byte{4, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(header[5:], uint16(len(payload)))
	nonce := make([]byte, aead.NonceSize())
	packet := append(salt, aead.Seal(nil, nonce, header, nil)...)
	binary.LittleEndian.PutUint64(nonce, 1)
	packet = append(packet, aead.Seal(nil, nonce, payload, nil)...)
	got, _, err := decodeQUICEnvelope(psk, packet)
	if err == nil {
		t.Fatalf("authenticated control host %q normalized into routable %v", host, got)
	}
}
