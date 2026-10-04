package command

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestClientAuthorityHistoryPrivateRPC(t *testing.T) {
	s, ctx := authorityService(t)
	p := clientpolicy.Policy{ClientID: "history-owner", Version: 1, Enabled: true, Multiplier: 2000000, BurstBytes: 16}
	if err := s.engine.Apply(p); err != nil {
		t.Fatal(err)
	}
	caps, err := s.GetCapabilities(ctx, &Empty{})
	if err != nil || !slices.Contains(caps.GetCapabilities(), "client-authority-history-v1") {
		t.Fatalf("private core cannot prove authority grant history: %v", err)
	}
	binding := &AuthorityBinding{AuthorityId: "issuer", Generation: 1, NodeId: "node"}
	if _, err := s.BindAuthority(ctx, &AuthorityBindRequest{ExpectedBootId: caps.BootId, Authority: binding}); err != nil {
		t.Fatal(err)
	}
	challenge, err := s.GetAuthorityChallenge(ctx, &AuthorityChallengeRequest{ExpectedBootId: caps.BootId})
	if err != nil {
		t.Fatal(err)
	}
	g := &ExecutionGrant{Authority: binding, InstanceId: caps.InstanceId, BootId: caps.BootId, ClientId: p.ClientID, WindowId: "window", PolicyVersion: 1, GrantId: "zero-used", Sequence: 1, ChallengeId: challenge.ChallengeId, Capacity: 40, Upload: &AuthorityShare{Unlimited: true}, Download: &AuthorityShare{Unlimited: true}, LeaseDurationMillis: 1000}
	if _, err := s.InstallAuthorityGrant(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SealAuthorityGrant(ctx, &AuthorityGrantRequest{ExpectedBootId: caps.BootId, ClientId: p.ClientID, GrantId: g.GrantId}); err != nil {
		t.Fatal(err)
	}
	state, err := s.GetClient(ctx, &ClientRequest{ClientId: p.ClientID})
	if err != nil || state.GetUsage().GetBilledBytes() != 0 || state.GetUncertainBytes() != 0 {
		t.Fatalf("zero-used history fixture failed: %+v/%v", state, err)
	}
	raw, err := protojson.MarshalOptions{EmitDefaultValues: true}.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || string(fields["authorityGrantHistory"]) != "true" {
		t.Fatal("private RPC erased zero-used sealed grant history")
	}
}
