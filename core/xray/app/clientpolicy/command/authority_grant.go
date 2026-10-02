package command

import (
	"context"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func authorityBinding(p *AuthorityBinding) clientpolicy.AuthorityBinding {
	return clientpolicy.AuthorityBinding{AuthorityID: p.GetAuthorityId(), Generation: p.GetGeneration(), NodeID: p.GetNodeId()}
}

func authorityShare(p *AuthorityShare) clientpolicy.AuthorityShare {
	return clientpolicy.AuthorityShare{Unlimited: p.GetUnlimited(), Rate: p.GetRate(), Burst: p.GetBurst()}
}

func runtimeGrant(p *ExecutionGrant) clientpolicy.ExecutionGrant {
	return clientpolicy.ExecutionGrant{Authority: authorityBinding(p.GetAuthority()), InstanceID: p.GetInstanceId(), BootID: p.GetBootId(), ClientID: p.GetClientId(), WindowID: p.GetWindowId(), PolicyVersion: p.GetPolicyVersion(), GrantID: p.GetGrantId(), Sequence: p.GetSequence(), ChallengeID: p.GetChallengeId(), Capacity: p.GetCapacity(), Upload: authorityShare(p.GetUpload()), Download: authorityShare(p.GetDownload()), LeaseDuration: time.Duration(p.GetLeaseDurationMillis()) * time.Millisecond}
}

func executionGrantState(state clientpolicy.ExecutionGrantState) *ExecutionGrantState {
	g := state.Grant
	return &ExecutionGrantState{Grant: &ExecutionGrant{Authority: &AuthorityBinding{AuthorityId: g.Authority.AuthorityID, Generation: g.Authority.Generation, NodeId: g.Authority.NodeID}, InstanceId: g.InstanceID, BootId: g.BootID, ClientId: g.ClientID, WindowId: g.WindowID, PolicyVersion: g.PolicyVersion, GrantId: g.GrantID, Sequence: g.Sequence, ChallengeId: g.ChallengeID, Capacity: g.Capacity, Upload: &AuthorityShare{Unlimited: g.Upload.Unlimited, Rate: g.Upload.Rate, Burst: g.Upload.Burst}, Download: &AuthorityShare{Unlimited: g.Download.Unlimited, Rate: g.Download.Rate, Burst: g.Download.Burst}, LeaseDurationMillis: uint64(g.LeaseDuration.Milliseconds())}, Usage: usage(state.Usage), Sequence: state.Sequence, Sealed: state.Sealed}
}

func (s *service) BindAuthority(ctx context.Context, r *AuthorityBindRequest) (*Empty, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.Authority == nil {
		return nil, status.Error(codes.InvalidArgument, "authority binding is required")
	}
	if err := s.engine.BindAuthority(r.ExpectedBootId, authorityBinding(r.Authority)); err != nil {
		return nil, rpcError(err)
	}
	return &Empty{}, nil
}

func (s *service) InstallAuthorityGrant(ctx context.Context, r *ExecutionGrant) (*ExecutionGrantState, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.Authority == nil || r.Upload == nil || r.Download == nil || r.LeaseDurationMillis == 0 || r.LeaseDurationMillis > uint64(clientpolicy.MaxAuthorityLeaseDuration.Milliseconds()) {
		return nil, status.Error(codes.InvalidArgument, "a complete bounded execution grant is required")
	}
	state, err := s.engine.InstallAuthorityGrant(runtimeGrant(r))
	if err != nil {
		return nil, rpcError(err)
	}
	return executionGrantState(state), nil
}

func (s *service) authorizeGrantRequest(ctx context.Context, r *AuthorityGrantRequest) error {
	if err := s.authorize(ctx); err != nil {
		return err
	}
	if r == nil || r.ClientId == "" || r.GrantId == "" {
		return status.Error(codes.InvalidArgument, "client and grant identities are required")
	}
	boot := s.engine.Capabilities().BootID
	if boot == "" || r.ExpectedBootId != boot {
		return rpcError(clientpolicy.ErrAuthority)
	}
	return nil
}

func (s *service) GetAuthorityGrant(ctx context.Context, r *AuthorityGrantRequest) (*ExecutionGrantState, error) {
	if err := s.authorizeGrantRequest(ctx, r); err != nil {
		return nil, err
	}
	state, err := s.engine.GetAuthorityGrant(r.ClientId)
	if err != nil {
		return nil, rpcError(err)
	}
	if state.Grant.GrantID != r.GrantId {
		return nil, rpcError(clientpolicy.ErrAuthority)
	}
	return executionGrantState(state), nil
}

func (s *service) SealAuthorityGrant(ctx context.Context, r *AuthorityGrantRequest) (*ExecutionGrantState, error) {
	if err := s.authorizeGrantRequest(ctx, r); err != nil {
		return nil, err
	}
	var state clientpolicy.ExecutionGrantState
	var err error
	if r.PreserveSessions {
		state, err = s.engine.PauseAuthorityGrant(r.ClientId, r.GrantId)
	} else {
		state, err = s.engine.SealAuthorityGrant(r.ClientId, r.GrantId)
	}
	if err != nil {
		return nil, rpcError(err)
	}
	return executionGrantState(state), nil
}

func (s *service) RenewAuthorityGrant(ctx context.Context, r *AuthorityRenewalRequest) (*Empty, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.ClientId == "" || r.GrantId == "" || r.ChallengeId == "" || r.Sequence == 0 || r.LeaseDurationMillis == 0 || r.LeaseDurationMillis > uint64(clientpolicy.MaxAuthorityLeaseDuration.Milliseconds()) {
		return nil, status.Error(codes.InvalidArgument, "a complete bounded grant renewal is required")
	}
	_, err := s.engine.RenewAuthorityGrant(clientpolicy.AuthorityGrantRenewal{BootID: r.ExpectedBootId, ClientID: r.ClientId, GrantID: r.GrantId, ChallengeID: r.ChallengeId, Sequence: r.Sequence, LeaseDuration: time.Duration(r.LeaseDurationMillis) * time.Millisecond})
	if err != nil {
		return nil, rpcError(err)
	}
	return &Empty{}, nil
}
