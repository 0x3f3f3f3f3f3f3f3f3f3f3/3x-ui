package command

import (
	"context"
	"math"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestPrivateAuthorityGrantRPCCommitsUsageAndSeal(t *testing.T) {
	s, ctx := authorityService(t)
	p := clientpolicy.Policy{ClientID: "owner", Version: 1, Enabled: true, Multiplier: 1500000, BurstBytes: 16}
	if err := s.engine.Apply(p); err != nil {
		t.Fatal(err)
	}
	caps, err := s.GetCapabilities(ctx, &Empty{})
	if err != nil {
		t.Fatal(err)
	}
	binding := &AuthorityBinding{AuthorityId: "issuer", Generation: 1, NodeId: "node"}
	if _, err := s.BindAuthority(ctx, &AuthorityBindRequest{ExpectedBootId: caps.BootId, Authority: binding}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	challenge, err := s.GetAuthorityChallenge(ctx, &AuthorityChallengeRequest{ExpectedBootId: caps.BootId})
	if err != nil {
		t.Fatal(err)
	}
	g := &ExecutionGrant{Authority: binding, InstanceId: caps.InstanceId, BootId: caps.BootId, ClientId: p.ClientID, WindowId: "window", PolicyVersion: p.Version, GrantId: "grant", Sequence: 1, ChallengeId: challenge.ChallengeId, Capacity: 32, Upload: &AuthorityShare{Unlimited: true}, Download: &AuthorityShare{Unlimited: true}, LeaseDurationMillis: 1000}
	first, err := s.InstallAuthorityGrant(ctx, g)
	if err != nil || !proto.Equal(first.GetGrant(), g) || first.GetSealed() {
		t.Fatalf("install: %+v/%v", first, err)
	}
	lease, err := s.engine.Open(context.Background(), clientpolicy.Metadata{ClientID: p.ClientID}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := lease.Admit(clientpolicy.Upload, 3); err != nil {
		t.Fatal(err)
	}
	request := &AuthorityGrantRequest{ExpectedBootId: caps.BootId, ClientId: p.ClientID, GrantId: g.GrantId}
	state, err := s.GetAuthorityGrant(ctx, request)
	if err != nil || state.GetUsage().GetRawUpload() != 3 || state.GetUsage().GetBilledBytes() != 4 || state.GetUsage().GetRemainder() != 500000 || state.GetSequence() == 0 {
		t.Fatalf("checkpoint: %+v/%v", state, err)
	}
	renewChallenge, err := s.GetAuthorityChallenge(ctx, &AuthorityChallengeRequest{ExpectedBootId: caps.BootId})
	if err != nil {
		t.Fatal(err)
	}
	renewal := &AuthorityRenewalRequest{ExpectedBootId: caps.BootId, ClientId: p.ClientID, GrantId: g.GrantId, ChallengeId: renewChallenge.ChallengeId, Sequence: 1, LeaseDurationMillis: 2000}
	for range 2 {
		if _, err := s.RenewAuthorityGrant(ctx, renewal); err != nil {
			t.Fatalf("private renewal: %v", err)
		}
	}
	sealed, err := s.SealAuthorityGrant(ctx, request)
	if err != nil || !sealed.GetSealed() || !proto.Equal(sealed.GetUsage(), state.GetUsage()) {
		t.Fatalf("seal: %+v/%v", sealed, err)
	}
	retry, err := s.SealAuthorityGrant(ctx, request)
	if err != nil || !proto.Equal(retry, sealed) {
		t.Fatalf("seal retry changed checkpoint: %+v/%v", retry, err)
	}
	if err := lease.Admit(clientpolicy.Upload, 1); err == nil {
		t.Fatal("sealed grant still admits payload")
	}
	request.ExpectedBootId = "retired"
	if _, err := s.SealAuthorityGrant(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale boot seal accepted: %v", err)
	}
}

func TestAuthorityGrantRPCRejectsNonprivateAndMalformedCalls(t *testing.T) {
	s, private := authorityService(t)
	checks := []func(context.Context) error{
		func(ctx context.Context) error { _, err := s.BindAuthority(ctx, nil); return err },
		func(ctx context.Context) error { _, err := s.InstallAuthorityGrant(ctx, nil); return err },
		func(ctx context.Context) error { _, err := s.GetAuthorityGrant(ctx, nil); return err },
		func(ctx context.Context) error { _, err := s.SealAuthorityGrant(ctx, nil); return err },
		func(ctx context.Context) error { _, err := s.RenewAuthorityGrant(ctx, nil); return err },
	}
	for i, check := range checks {
		if err := check(context.Background()); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("call %d nonprivate: %v", i, err)
		}
		if err := check(private); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("call %d nil input: %v", i, err)
		}
	}
	if _, err := s.InstallAuthorityGrant(private, &ExecutionGrant{LeaseDurationMillis: math.MaxUint64}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("duration overflow accepted: %v", err)
	}
}
