package mieru_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/proxy/mieru"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

type heldOutbound struct {
	up     *pipe.Writer
	down   *pipe.Reader
	done   <-chan error
	cancel context.CancelFunc
}

func holdOutbound(t *testing.T, client *mieru.Client, dialer *reviewCountingDialer, target xnet.Destination, user *protocol.MemoryUser, tag string) *heldOutbound {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ctx = session.ContextWithInbound(ctx, &session.Inbound{User: user, Tag: tag})
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target}})
	upReader, upWriter := pipe.New()
	downReader, downWriter := pipe.New()
	done := make(chan error, 1)
	go func() { done <- client.Process(ctx, &transport.Link{Reader: upReader, Writer: downWriter}, dialer) }()
	held := &heldOutbound{up: upWriter, down: downReader, done: done, cancel: cancel}
	t.Cleanup(func() { cancel(); _ = upWriter.Close(); downReader.Interrupt() })
	held.exchange(t)
	return held
}

func (h *heldOutbound) exchange(t *testing.T) {
	t.Helper()
	payload := []byte("authenticated-outbound-payload")
	if err := h.up.WriteMultiBuffer(buf.MergeBytes(nil, payload)); err != nil {
		t.Fatal(err)
	}
	mb, err := h.down.ReadMultiBufferTimeout(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer buf.ReleaseMulti(mb)
	result := make([]byte, mb.Len())
	mb.Copy(result)
	if !bytes.Equal(result, payload) {
		t.Fatalf("outbound payload %q", result)
	}
}

func (h *heldOutbound) ended(t *testing.T) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("outbound process survived cancellation or credential removal")
	}
}

func TestNativeMieruOutboundPoolIsolatesAuthenticationAndCancellation(t *testing.T) {
	port := reservePort(t, "TCP")
	referenceServer(t, port, "TCP")
	client, err := mieru.NewClient(context.Background(), &mieru.ClientConfig{Address: xnet.NewIPOrDomain(xnet.LocalHostIP), Port: uint32(port), Username: "alice", Password: "native-business-secret", Transport: "TCP", Multiplexing: "MULTIPLEXING_HIGH"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	target := xnet.DestinationFromAddr(echoTarget(t, "tcp"))
	dialer := &reviewCountingDialer{}
	alice := &protocol.MemoryUser{ClientID: nativeClientID, Email: "alice"}
	bob := &protocol.MemoryUser{ClientID: "22222222-2222-4222-8222-222222222222", Email: "bob"}
	a := holdOutbound(t, client, dialer, target, alice, "native-in")
	sibling := holdOutbound(t, client, dialer, target, alice, "native-in")
	beforeBob := dialer.count.Load()
	b := holdOutbound(t, client, dialer, target, bob, "native-in")
	if dialer.count.Load() != beforeBob+1 {
		t.Fatal("different canonical user reused Alice's authenticated transport")
	}
	beforeTag := dialer.count.Load()
	otherTag := holdOutbound(t, client, dialer, target, bob, "other-in")
	if dialer.count.Load() != beforeTag+1 {
		t.Fatal("different inbound tag reused a retained transport context")
	}
	otherDialer := &reviewCountingDialer{}
	otherGateway := holdOutbound(t, client, otherDialer, target, bob, "native-in")
	if otherDialer.count.Load() != 1 {
		t.Fatal("different supplied dialer did not create its own transport")
	}
	a.cancel()
	a.ended(t)
	sibling.exchange(t)
	alice.RevokeCredential()
	sibling.ended(t)
	b.exchange(t)
	otherTag.exchange(t)
	otherGateway.exchange(t)
	replacement := &protocol.MemoryUser{ClientID: nativeClientID, Email: "alice"}
	beforeReplacement := dialer.count.Load()
	fresh := holdOutbound(t, client, dialer, target, replacement, "native-in")
	if dialer.count.Load() != beforeReplacement+1 {
		t.Fatal("replacement credential reused a retired transport")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	b.ended(t)
	otherTag.ended(t)
	otherGateway.ended(t)
	fresh.ended(t)
}
