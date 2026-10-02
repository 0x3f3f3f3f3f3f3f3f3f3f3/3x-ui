package xray

import (
	"context"
	"errors"
	"testing"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/grpc"
)

type authorityAdapterProbe struct {
	command.ClientPolicyServiceClient
	boot  string
	calls int
}

func (p *authorityAdapterProbe) BindAuthority(_ context.Context, r *command.AuthorityBindRequest, _ ...grpc.CallOption) (*command.Empty, error) {
	p.boot = r.ExpectedBootId
	p.calls++
	return &command.Empty{}, nil
}
func (p *authorityAdapterProbe) GetAuthorityChallenge(_ context.Context, r *command.AuthorityChallengeRequest, _ ...grpc.CallOption) (*command.AuthorityChallenge, error) {
	p.boot = r.ExpectedBootId
	p.calls++
	return &command.AuthorityChallenge{InstanceId: "source", BootId: p.boot, ChallengeId: "challenge", MaxDurationMillis: 10000}, nil
}
func (p *authorityAdapterProbe) InstallAuthorityGrant(_ context.Context, g *command.ExecutionGrant, _ ...grpc.CallOption) (*command.ExecutionGrantState, error) {
	p.boot = g.BootId
	p.calls++
	return &command.ExecutionGrantState{Grant: g}, nil
}
func (p *authorityAdapterProbe) GetAuthorityGrant(_ context.Context, r *command.AuthorityGrantRequest, _ ...grpc.CallOption) (*command.ExecutionGrantState, error) {
	p.boot = r.ExpectedBootId
	p.calls++
	return &command.ExecutionGrantState{Grant: &command.ExecutionGrant{BootId: p.boot, ClientId: r.ClientId, GrantId: r.GrantId}}, nil
}
func (p *authorityAdapterProbe) SealAuthorityGrant(_ context.Context, r *command.AuthorityGrantRequest, _ ...grpc.CallOption) (*command.ExecutionGrantState, error) {
	state, err := p.GetAuthorityGrant(context.Background(), r)
	state.Sealed = true
	return state, err
}

func TestAuthorityAdapterCarriesNegotiatedBootAndRejectsUnsupportedCalls(t *testing.T) {
	boot := "0123456789abcdef0123456789abcdef"
	probe := &authorityAdapterProbe{}
	api := &ClientPolicyAPI{client: probe, capabilities: &command.Capabilities{InstanceId: "source", BootId: boot, Capabilities: []string{"fresh-core-incarnation-v1", "monotonic-authority-challenge-v1", "boot-bound-execution-grants-v1"}}}
	ctx := context.Background()
	if err := api.BindAuthority(ctx, &command.AuthorityBinding{AuthorityId: "issuer", Generation: 1, NodeId: "node"}); err != nil {
		t.Fatal(err)
	}
	challenge, err := api.AuthorityChallenge(ctx)
	if err != nil || challenge.GetBootId() != boot {
		t.Fatalf("challenge: %+v/%v", challenge, err)
	}
	g := &command.ExecutionGrant{InstanceId: "source", BootId: boot, ClientId: "owner", GrantId: "grant"}
	if state, err := api.InstallAuthorityGrant(ctx, g); err != nil || state.GetGrant().GetBootId() != boot {
		t.Fatalf("install: %+v/%v", state, err)
	}
	if state, err := api.GetAuthorityGrant(ctx, "owner", "grant"); err != nil || state.GetGrant().GetGrantId() != "grant" {
		t.Fatalf("get: %+v/%v", state, err)
	}
	if state, err := api.SealAuthorityGrant(ctx, "owner", "grant"); err != nil || !state.GetSealed() {
		t.Fatalf("seal: %+v/%v", state, err)
	}
	if probe.calls != 5 || probe.boot != boot {
		t.Fatalf("lost boot binding: %+v", probe)
	}
	g.BootId = "retired"
	if _, err := api.InstallAuthorityGrant(ctx, g); !errors.Is(err, ErrClientPolicyCapability) || probe.calls != 5 {
		t.Fatalf("sent retired grant to child: %v/%d", err, probe.calls)
	}
	api.capabilities.Capabilities = nil
	for _, call := range []func() error{
		func() error { return api.BindAuthority(ctx, nil) },
		func() error { _, err := api.AuthorityChallenge(ctx); return err },
		func() error { _, err := api.InstallAuthorityGrant(ctx, g); return err },
		func() error { _, err := api.GetAuthorityGrant(ctx, "owner", "grant"); return err },
		func() error { _, err := api.SealAuthorityGrant(ctx, "owner", "grant"); return err },
	} {
		if err := call(); !errors.Is(err, ErrClientPolicyCapability) {
			t.Fatalf("unsupported authority call: %v", err)
		}
	}
	if probe.calls != 5 {
		t.Fatal("unsupported core received authority mutation")
	}
}
