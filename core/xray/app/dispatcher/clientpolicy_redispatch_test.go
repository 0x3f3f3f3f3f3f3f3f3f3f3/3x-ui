package dispatcher_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
)

type redispatchByteReader struct{}

func (*redispatchByteReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	return buf.MergeBytes(nil, []byte("a")), nil
}

func TestManagedRedispatchTimeoutIncludesRateAdmission(t *testing.T) {
	engine := clientpolicy.NewEngine()
	defer engine.Close()
	user := &protocol.MemoryUser{ClientID: "owner"}
	if err := engine.Apply(clientpolicy.Policy{ClientID: user.ClientID, Version: 1, Enabled: true, Multiplier: 1000000, UploadRate: 1, BurstBytes: 1}); err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: user})
	link := &transport.Link{Reader: &redispatchByteReader{}, Writer: buf.Discard}
	ctx, release, err := dispatcher.ManageLinkForTest(engine, ctx, net.TCPDestination(net.LocalHostIP, 1234), link)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mb, err := link.Reader.ReadMultiBuffer()
	if err != nil {
		t.Fatal(err)
	}
	buf.ReleaseMulti(mb)
	link = dispatcher.WrapLink(ctx, nil, nil, link)
	reader := link.Reader.(buf.TimeoutReader)
	started := time.Now()
	mb, err = reader.ReadMultiBufferTimeout(20 * time.Millisecond)
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond || !mb.IsEmpty() || err != nil {
		buf.ReleaseMulti(mb)
		t.Fatalf("sniff timeout waited for the shared rate limiter: elapsed=%v err=%v", elapsed, err)
	}
	mb, err = reader.ReadMultiBuffer()
	if err != nil || mb.Len() != 1 {
		t.Fatalf("timed-out read lost its pending payload: %d %v", mb.Len(), err)
	}
	buf.ReleaseMulti(mb)
	snapshot, err := engine.Snapshot(user.ClientID)
	if err != nil || snapshot.Usage != (clientpolicy.Usage{RawUpload: 2, BilledBytes: 2}) {
		t.Fatalf("pending read charged more than once: %+v %v", snapshot, err)
	}
}

func TestManagedRedispatchKeepsIndependentLinksAndCredentialRevocation(t *testing.T) {
	engine := clientpolicy.NewEngine()
	defer engine.Close()
	user := &protocol.MemoryUser{ClientID: "owner"}
	if err := engine.Apply(clientpolicy.Policy{ClientID: user.ClientID, Version: 1, Enabled: true, Multiplier: 1500000, BurstBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	target := net.TCPDestination(net.LocalHostIP, 1234)
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: user, Tag: "original"})
	first := &transport.Link{Reader: buf.NewReader(bytes.NewBufferString("abcdef")), Writer: buf.Discard}
	ctx, release, err := dispatcher.ManageLinkForTest(engine, ctx, target, first)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx = session.ContextWithInbound(ctx, &session.Inbound{User: user, Tag: "loopback"})
	ctx, finishHop, err := dispatcher.ManageLinkForTest(engine, ctx, target, first)
	if err != nil {
		t.Fatal(err)
	}
	defer finishHop()
	second := &transport.Link{Reader: buf.NewReader(bytes.NewBufferString("ghijkl")), Writer: buf.Discard}
	_, releaseSecond, err := dispatcher.ManageLinkForTest(engine, ctx, target, second)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	for _, link := range []*transport.Link{first, second} {
		mb, err := link.Reader.ReadMultiBuffer()
		if err != nil || mb.Len() != 6 {
			t.Fatalf("read independent payload: %d %v", mb.Len(), err)
		}
		if err := link.Writer.WriteMultiBuffer(mb); err != nil {
			t.Fatal(err)
		}
	}
	finishHop()
	snapshot, err := engine.Snapshot(user.ClientID)
	if err != nil || snapshot.ActiveSessions != 2 || snapshot.Usage != (clientpolicy.Usage{RawUpload: 12, RawDownload: 12, BilledBytes: 36}) {
		t.Fatalf("same-link hop or new nested link changed accounting: %+v %v", snapshot, err)
	}
	user.RevokeCredential()
	for _, link := range []*transport.Link{first, second} {
		if err := link.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("revoked"))); err == nil {
			t.Fatal("revoked credential admitted payload")
		}
	}
	snapshot, err = engine.Snapshot(user.ClientID)
	if err != nil || snapshot.ActiveSessions != 0 || snapshot.Usage != (clientpolicy.Usage{RawUpload: 12, RawDownload: 12, BilledBytes: 36}) {
		t.Fatalf("revocation lost or charged the inherited session: %+v %v", snapshot, err)
	}
}

func TestManagedRedispatchRejectsIdentityChanges(t *testing.T) {
	for _, name := range []string{"removed-inbound", "removed-user", "unmanaged-user", "other-client", "other-credential"} {
		t.Run(name, func(t *testing.T) {
			engine := clientpolicy.NewEngine()
			defer engine.Close()
			for _, id := range []string{"owner", "other"} {
				if err := engine.Apply(clientpolicy.Policy{ClientID: id, Version: 1, Enabled: true, Multiplier: 1000000, BurstBytes: 1024}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, link, release, err := managedCredentialLink(engine, &protocol.MemoryUser{ClientID: "owner"}, "original")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			in := &session.Inbound{}
			switch name {
			case "removed-inbound":
				in = nil
			case "unmanaged-user":
				in.User = &protocol.MemoryUser{}
			case "other-client":
				in.User = &protocol.MemoryUser{ClientID: "other"}
			case "other-credential":
				in.User = &protocol.MemoryUser{ClientID: "owner"}
			}
			ctx = session.ContextWithInbound(ctx, in)
			_, finish, err := dispatcher.ManageLinkForTest(engine, ctx, net.TCPDestination(net.LocalHostIP, 1234), link)
			if finish != nil {
				finish()
			}
			if err == nil {
				t.Fatal("same link accepted a changed authenticated identity")
			}
		})
	}
}
