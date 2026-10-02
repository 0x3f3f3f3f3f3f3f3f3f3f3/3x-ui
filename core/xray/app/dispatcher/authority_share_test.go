package dispatcher_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
)

func TestGrantedStreamUsesDirectionalShareSmallerThanBuffer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := clientpolicy.CreateStore(path, "stream-source"); err != nil {
		t.Fatal(err)
	}
	object, err := common.CreateObject(context.Background(), &clientpolicy.Config{StateFile: path, InstanceId: "stream-source"})
	if err != nil {
		t.Fatal(err)
	}
	e := object.(*clientpolicy.Engine)
	defer e.Close()
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	p := clientpolicy.Policy{ClientID: "owner", Version: 1, Enabled: true, Multiplier: 2000000, BurstBytes: 64, UploadRate: 512, DownloadRate: 512}
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	identity := clientpolicy.AuthorityBinding{AuthorityID: "issuer", Generation: 1, NodeID: "node-a"}
	boot := e.Capabilities().BootID
	if err := e.BindAuthority(boot, identity); err != nil {
		t.Fatal(err)
	}
	challenge, err := e.BeginAuthorityChallenge(boot)
	if err != nil {
		t.Fatal(err)
	}
	share := clientpolicy.AuthorityShare{Rate: 512, Burst: 8}
	g := clientpolicy.ExecutionGrant{Authority: identity, InstanceID: "stream-source", BootID: boot, ClientID: p.ClientID, WindowID: "window", PolicyVersion: 1, GrantID: "grant", Sequence: 1, ChallengeID: challenge.ChallengeID, Capacity: 512, Upload: share, Download: share, LeaseDuration: time.Second}
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("stream-payload-"), 4)
	var delivered bytes.Buffer
	link := &transport.Link{Reader: buf.NewReader(bytes.NewReader(payload)), Writer: buf.NewWriter(&delivered)}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: &protocol.MemoryUser{ClientID: p.ClientID}, Tag: "owned"})
	_, release, err := dispatcher.ManageLinkForTest(e, ctx, net.TCPDestination(net.LocalHostIP, 1234), link)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := buf.Copy(link.Reader, link.Writer); err != nil {
		t.Fatalf("granted stream larger than share burst could not flow: %v", err)
	}
	if !bytes.Equal(delivered.Bytes(), payload) {
		t.Fatalf("granted stream lost/reordered bytes: got%d want%d", delivered.Len(), len(payload))
	}
	snapshot, err := e.Snapshot(p.ClientID)
	if err != nil || snapshot.Usage.RawUpload != uint64(len(payload)) || snapshot.Usage.RawDownload != uint64(len(payload)) || snapshot.Usage.BilledBytes != uint64(len(payload))*4 {
		t.Fatalf("split grant stream changed accounting: %+v/%v", snapshot, err)
	}
}

type emptyOwnedReader struct{}

func (emptyOwnedReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	return buf.MultiBuffer{buf.New()}, io.EOF
}

func TestManagedEmptyReadDoesNotRetainReturnedBufferOwnership(t *testing.T) {
	e := clientpolicy.NewEngine()
	defer e.Close()
	if err := e.Apply(clientpolicy.Policy{ClientID: "owner", Version: 1, Enabled: true, Multiplier: 1000000, BurstBytes: 65536}); err != nil {
		t.Fatal(err)
	}
	link := &transport.Link{Reader: emptyOwnedReader{}, Writer: buf.NewWriter(io.Discard)}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: &protocol.MemoryUser{ClientID: "owner"}})
	_, release, err := dispatcher.ManageLinkForTest(e, ctx, net.TCPDestination(net.LocalHostIP, 1234), link)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mb, err := link.Reader.ReadMultiBuffer()
	defer buf.ReleaseMulti(mb)
	if err != io.EOF {
		t.Fatalf("empty read error: %v", err)
	}
	common.Interrupt(link.Reader)
	for _, b := range mb {
		if b == nil {
			t.Fatal("interrupt reclaimed an already returned buffer")
		}
	}
}
