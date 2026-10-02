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
	page  *command.AuthorityRequests
}

func (p *authorityAdapterProbe) EnableAuthorityRequests(_ context.Context, r *command.AuthorityBindRequest, _ ...grpc.CallOption) (*command.Empty, error) {
	p.boot = r.ExpectedBootId
	p.calls++
	return &command.Empty{}, nil
}

func (p *authorityAdapterProbe) ReadAuthorityRequests(_ context.Context, r *command.AuthorityRequestsRequest, _ ...grpc.CallOption) (*command.AuthorityRequests, error) {
	p.boot = r.ExpectedBootId
	p.calls++
	if p.page != nil {
		return p.page, nil
	}
	return &command.AuthorityRequests{InstanceId: "source", BootId: p.boot, Requests: []*command.AuthorityRequest{{RequestId: "0123456789abcdef0123456789abcdef", ClientId: "owner", PolicyVersion: 1}}}, nil
}

func TestAuthorityDemandAdapterBindsCurrentBootAndRejectsContradictoryReplies(t *testing.T) {
	boot := "0123456789abcdef0123456789abcdef"
	p := &authorityAdapterProbe{}
	api := &ClientPolicyAPI{client: p, capabilities: &command.Capabilities{InstanceId: "source", BootId: boot, Capabilities: []string{"fresh-core-incarnation-v1", "monotonic-authority-challenge-v1", "boot-bound-execution-grants-v1", "on-demand-authority-requests-v1"}}}
	binding := &command.AuthorityBinding{AuthorityId: "issuer", Generation: 1, NodeId: "node"}
	if err := api.EnableAuthorityRequests(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	page, err := api.ReadAuthorityRequests(context.Background(), binding, 128)
	if err != nil || page.GetBootId() != boot || p.calls != 2 || p.boot != boot {
		t.Fatalf("demand adapter lost boot binding: %+v/%v/%d", page, err, p.calls)
	}
	for _, fault := range []string{"boot", "source", "duplicates", "version", "request-id"} {
		p.page = &command.AuthorityRequests{InstanceId: "source", BootId: boot, Requests: []*command.AuthorityRequest{{RequestId: "0123456789abcdef0123456789abcdef", ClientId: "owner", PolicyVersion: 1}}}
		switch fault {
		case "boot":
			p.page.BootId = "copied-boot"
		case "source":
			p.page.InstanceId = "another-source"
		case "duplicates":
			p.page.Requests = append(p.page.Requests, p.page.Requests[0])
		case "version":
			p.page.Requests[0].PolicyVersion = 0
		case "request-id":
			p.page.Requests[0].RequestId = "malformed"
		}
		if page, err := api.ReadAuthorityRequests(context.Background(), binding, 128); !errors.Is(err, ErrClientPolicyCapability) || page != nil {
			t.Fatalf("%s contradictory demand reply passed: %+v/%v", fault, page, err)
		}
	}
	calls := p.calls
	if _, err := api.ReadAuthorityRequests(context.Background(), binding, 129); !errors.Is(err, ErrClientPolicyCapability) || p.calls != calls {
		t.Fatalf("unbounded demand reached core: %v/%d", err, p.calls)
	}
	api.capabilities.Capabilities = api.capabilities.Capabilities[:3]
	if err := api.EnableAuthorityRequests(context.Background(), binding); !errors.Is(err, ErrClientPolicyCapability) || p.calls != calls {
		t.Fatalf("unsupported core enabled demand: %v/%d", err, p.calls)
	}
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

func (p *authorityAdapterProbe) RenewAuthorityGrant(_ context.Context, r *command.AuthorityRenewalRequest, _ ...grpc.CallOption) (*command.Empty, error) {
	p.boot = r.ExpectedBootId
	p.calls++
	return &command.Empty{}, nil
}

func TestAuthorityAdapterRenewsOnlyCurrentCapableBoot(t *testing.T) {
	boot := "0123456789abcdef0123456789abcdef"
	p := &authorityAdapterProbe{}
	c := &ClientPolicyAPI{client: p, capabilities: &command.Capabilities{InstanceId: "source", BootId: boot, Capabilities: []string{"fresh-core-incarnation-v1", "monotonic-authority-challenge-v1", "boot-bound-execution-grants-v1", "monotonic-grant-renewal-v1"}}}
	r := &command.AuthorityRenewalRequest{ExpectedBootId: boot, ClientId: "owner", GrantId: "grant", ChallengeId: "challenge", Sequence: 1, LeaseDurationMillis: 1000}
	if err := c.RenewAuthorityGrant(context.Background(), r); err != nil || p.calls != 1 || p.boot != boot {
		t.Fatalf("renewal: %v/%+v", err, p)
	}
	r.ExpectedBootId = "retired"
	if err := c.RenewAuthorityGrant(context.Background(), r); !errors.Is(err, ErrClientPolicyCapability) || p.calls != 1 {
		t.Fatalf("retired renewal sent: %v/%d", err, p.calls)
	}
	r.ExpectedBootId = boot
	c.capabilities.Capabilities = c.capabilities.Capabilities[:3]
	if err := c.RenewAuthorityGrant(context.Background(), r); !errors.Is(err, ErrClientPolicyCapability) || p.calls != 1 {
		t.Fatalf("unsupported renewal sent: %v/%d", err, p.calls)
	}
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
