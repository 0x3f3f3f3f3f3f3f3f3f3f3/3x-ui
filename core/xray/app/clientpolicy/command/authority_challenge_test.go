package command

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func authorityService(t *testing.T) (*service, context.Context) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	if err := clientpolicy.CreateStore(path, "rpc-source"); err != nil {
		t.Fatal(err)
	}
	object, err := common.CreateObject(context.Background(), &clientpolicy.Config{StateFile: path, InstanceId: "rpc-source"})
	if err != nil {
		t.Fatal(err)
	}
	engine := object.(*clientpolicy.Engine)
	t.Cleanup(func() { _ = engine.Close() })
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.UnixAddr{Net: "unix", Name: "owned-test-control"}})
	return &service{engine: engine}, ctx
}

func TestAuthorityChallengeRPCBindsCurrentConfiguredBoot(t *testing.T) {
	s, ctx := authorityService(t)
	caps, err := s.GetCapabilities(ctx, &Empty{})
	if err != nil || len(caps.GetBootId()) != 32 {
		t.Fatalf("configured nonce missing from capabilities: %+v/%v", caps, err)
	}
	challenge, err := s.GetAuthorityChallenge(ctx, &AuthorityChallengeRequest{ExpectedBootId: caps.BootId})
	if err != nil || challenge.GetBootId() != caps.BootId || challenge.GetInstanceId() != caps.InstanceId || len(challenge.GetChallengeId()) != 32 || challenge.GetMaxDurationMillis() != 10000 {
		t.Fatalf("challenge not bound to current source/boot: %+v/%v", challenge, err)
	}
	if _, err := s.GetAuthorityChallenge(ctx, &AuthorityChallengeRequest{ExpectedBootId: "retired"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("retired boot challenge accepted: %v", err)
	}
}

func TestAuthorityChallengeRPCRequiresPrivatePeer(t *testing.T) {
	s, _ := authorityService(t)
	for _, ctx := range []context.Context{context.Background(), peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}})} {
		if _, err := s.GetAuthorityChallenge(ctx, &AuthorityChallengeRequest{ExpectedBootId: s.engine.Capabilities().BootID}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("nonprivate caller requested challenge: %v", err)
		}
	}
}
