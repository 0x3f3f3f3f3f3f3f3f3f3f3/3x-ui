package command

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestPolicyRPCNeverAcceptsTCPOrMissingPeer(t *testing.T) {
	s := new(service)
	for _, ctx := range []context.Context{context.Background(), peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}})} {
		if _, err := s.GetCapabilities(ctx, &Empty{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("accepted non-Unix caller: %v", err)
		}
	}
}
