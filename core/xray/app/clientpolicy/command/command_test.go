package command

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestCloseConnectionsRestrictsItsInboundScope(t *testing.T) {
	engine := clientpolicy.NewEngine()
	t.Cleanup(func() { _ = engine.Close() })
	if err := engine.Apply(clientpolicy.Policy{ClientID: "shared", Version: 1, Enabled: true, Multiplier: 1000000, BurstBytes: 65536}); err != nil {
		t.Fatal(err)
	}
	first, err := engine.Open(context.Background(), clientpolicy.Metadata{ClientID: "shared", InboundTag: "first"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Open(context.Background(), clientpolicy.Metadata{ClientID: "shared", InboundTag: "second"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &service{engine: engine}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.UnixAddr{Net: "unix", Name: "test-private"}})
	closed, err := s.CloseConnections(ctx, &ClientRequest{ClientId: "shared", InboundTag: "first"})
	if err != nil || closed.GetClosed() != 1 {
		t.Fatalf("scoped close affected the wrong sessions: %+v %v", closed, err)
	}
	if err := first.Admit(clientpolicy.Upload, 7); !errors.Is(err, clientpolicy.ErrSessionClosed) {
		t.Fatalf("removed inbound still admits payload: %v", err)
	}
	if err := second.Admit(clientpolicy.Upload, 11); err != nil {
		t.Fatalf("sibling inbound stopped accepting payload: %v", err)
	}
	for _, tag := range []string{"first", "unknown"} {
		closed, err := s.CloseConnections(ctx, &ClientRequest{ClientId: "shared", InboundTag: tag})
		if err != nil || closed.GetClosed() != 0 {
			t.Fatalf("absent scoped sessions affected a sibling: %+v %v", closed, err)
		}
	}
	closed, err = s.CloseConnections(ctx, &ClientRequest{ClientId: "shared"})
	if err != nil || closed.GetClosed() != 1 {
		t.Fatalf("legacy unscoped close did not close the remaining session: %+v %v", closed, err)
	}
	if err := second.Admit(clientpolicy.Download, 5); !errors.Is(err, clientpolicy.ErrSessionClosed) {
		t.Fatalf("global close retained a session: %v", err)
	}
	snapshot, err := engine.Snapshot("shared")
	if err != nil || snapshot.Usage.RawUpload != 11 || snapshot.Usage.RawDownload != 0 || snapshot.Usage.BilledBytes != 11 || snapshot.ActiveSessions != 0 {
		t.Fatalf("connection removal changed accounting: %+v %v", snapshot, err)
	}
}

func TestPolicyRPCNeverAcceptsTCPOrMissingPeer(t *testing.T) {
	s := new(service)
	for _, ctx := range []context.Context{context.Background(), peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}})} {
		if _, err := s.GetCapabilities(ctx, &Empty{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("accepted non-Unix caller: %v", err)
		}
	}
}
