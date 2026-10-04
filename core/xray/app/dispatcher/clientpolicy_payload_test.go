package dispatcher_test

import (
	"bytes"
	"context"
	"io"
	stdnet "net"
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

func TestManagedWriterDeliversFinalQuotaByteBeforeClosingConnection(t *testing.T) {
	engine := clientpolicy.NewEngine()
	defer engine.Close()
	user := &protocol.MemoryUser{ClientID: "paid-last-byte"}
	if err := engine.Apply(clientpolicy.Policy{ClientID: user.ClientID, Version: 1, Enabled: true, Multiplier: 1000000, QuotaBytes: 2, BurstBytes: 65536}); err != nil {
		t.Fatal(err)
	}
	server, client := stdnet.Pipe()
	defer server.Close()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: user, Conn: server})
	link := &transport.Link{Reader: buf.NewReader(bytes.NewBufferString("u")), Writer: buf.NewWriter(server)}
	_, release, err := dispatcher.ManageLinkForTest(engine, ctx, net.TCPDestination(net.LocalHostIP, 1234), link)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mb, err := link.Reader.ReadMultiBuffer()
	if err != nil || mb.Len() != 1 {
		t.Fatal("initial upload failed", err)
	}
	buf.ReleaseMulti(mb)
	delivered := make(chan error, 1)
	go func() {
		var raw [1]byte
		_, err := io.ReadFull(client, raw[:])
		if err == nil && raw[0] != 'd' {
			err = io.ErrUnexpectedEOF
		}
		delivered <- err
	}()
	if err := link.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("d"))); err != nil {
		t.Fatal("paid final download closed before write", err)
	}
	if err := <-delivered; err != nil {
		t.Fatal("paid final byte did not reach peer", err)
	}
	mb, err = link.Reader.ReadMultiBuffer()
	buf.ReleaseMulti(mb)
	if err == nil {
		t.Fatal("exhausted reader returned another payload")
	}
	snapshot, err := engine.Snapshot(user.ClientID)
	if err != nil || snapshot.Usage.RawUpload != 1 || snapshot.Usage.RawDownload != 1 || snapshot.ActiveSessions != 0 {
		t.Fatalf("last payload did not close and settle: %+v/%v", snapshot, err)
	}
}

func TestManagedReaderRetainsFinalQuotaByteForItsForwarder(t *testing.T) {
	engine := clientpolicy.NewEngine()
	defer engine.Close()
	user := &protocol.MemoryUser{ClientID: "paid-last-upload"}
	if err := engine.Apply(clientpolicy.Policy{ClientID: user.ClientID, Version: 1, Enabled: true, Multiplier: 1000000, QuotaBytes: 1, BurstBytes: 65536}); err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: user})
	link := &transport.Link{Reader: buf.NewReader(bytes.NewBufferString("u")), Writer: buf.Discard}
	managed, release, err := dispatcher.ManageLinkForTest(engine, ctx, net.TCPDestination(net.LocalHostIP, 1234), link)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mb, err := link.Reader.ReadMultiBuffer()
	if err != nil || mb.Len() != 1 || managed.Err() != nil {
		buf.ReleaseMulti(mb)
		t.Fatal("last paid upload closed before forwarder received its payload", err, managed.Err())
	}
	buf.ReleaseMulti(mb)
	mb, err = link.Reader.ReadMultiBuffer()
	buf.ReleaseMulti(mb)
	if err == nil {
		t.Fatal("quota reader admitted another payload")
	}
	snapshot, err := engine.Snapshot(user.ClientID)
	if err != nil || snapshot.ActiveSessions != 0 || snapshot.Usage.RawUpload != 1 {
		t.Fatalf("forwarder completion did not terminate exhausted reader: %+v/%v", snapshot, err)
	}
}
