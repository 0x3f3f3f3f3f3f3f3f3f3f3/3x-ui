package command

import (
	"context"
	"errors"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type service struct {
	UnimplementedClientPolicyServiceServer
	engine clientpolicy.Manager
}

func (*service) RequiresPrivateUnixSocket() bool { return true }
func (s *service) Register(server *grpc.Server)  { RegisterClientPolicyServiceServer(server, s) }

func (s *service) authorize(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil || p.Addr.Network() != "unix" {
		return status.Error(codes.PermissionDenied, "client policy requires a private Unix socket")
	}
	if s.engine == nil || !s.engine.Capabilities().Ready {
		return status.Error(codes.FailedPrecondition, "client policy engine not ready")
	}
	return nil
}

func rpcError(err error) error {
	if err == nil {
		return nil
	}
	code := codes.Internal
	switch {
	case errors.Is(err, clientpolicy.ErrUnknownClient):
		code = codes.NotFound
	case errors.Is(err, clientpolicy.ErrPolicyVersion):
		code = codes.Aborted
	case errors.Is(err, clientpolicy.ErrInvalidPolicy), errors.Is(err, clientpolicy.ErrInvalidUsage):
		code = codes.InvalidArgument
	case errors.Is(err, clientpolicy.ErrRevoked), errors.Is(err, clientpolicy.ErrEngineClosed), errors.Is(err, clientpolicy.ErrStorage), errors.Is(err, clientpolicy.ErrLedgerCursor), errors.Is(err, clientpolicy.ErrAlreadyInitialized):
		code = codes.FailedPrecondition
	case errors.Is(err, clientpolicy.ErrQueueFull):
		code = codes.ResourceExhausted
	}
	return status.Error(code, err.Error())
}

func (s *service) GetCapabilities(ctx context.Context, _ *Empty) (*Capabilities, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	c := s.engine.Capabilities()
	features := []string{"trusted-tunnel-client-id-v1", "trusted-vless-client-id-v1", "trusted-vmess-client-id-v1", "trusted-trojan-client-id-v1", "trusted-shadowsocks-aead-client-id-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "quota-window-baseline-v1", "live-session-control-v1", "inbound-scoped-session-close-v1", "authenticated-credential-revocation-v1"}
	if c.Persistent {
		features = append(features, "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1", "durable-first-use-expiry-v1")
	}
	return &Capabilities{ApiVersion: 1, CoreVersion: core.VersionStatement()[0], InstanceId: c.InstanceID, Epoch: c.Epoch, Capabilities: features, ReservationRawBytes: c.ReservationRawBytes}, nil
}

func policyConfig(p clientpolicy.Policy) *clientpolicy.PolicyConfig {
	return &clientpolicy.PolicyConfig{ClientId: p.ClientID, Version: p.Version, Enabled: p.Enabled, MultiplierMicros: p.Multiplier, QuotaBytes: p.QuotaBytes, UploadBytesPerSecond: p.UploadRate, DownloadBytesPerSecond: p.DownloadRate, BurstBytes: p.BurstBytes, ExpiresAt: p.ExpiresAt, QuotaBaselineBytes: p.QuotaBaselineBytes, QuotaBaselineRemainder: p.QuotaBaselineRemainder}
}

func runtimePolicy(p *clientpolicy.PolicyConfig) clientpolicy.Policy {
	return clientpolicy.Policy{ClientID: p.ClientId, Version: p.Version, Enabled: p.Enabled, Multiplier: p.MultiplierMicros, QuotaBytes: p.QuotaBytes, UploadRate: p.UploadBytesPerSecond, DownloadRate: p.DownloadBytesPerSecond, BurstBytes: p.BurstBytes, ExpiresAt: p.ExpiresAt, QuotaBaselineBytes: p.QuotaBaselineBytes, QuotaBaselineRemainder: p.QuotaBaselineRemainder}
}

func (s *service) InitializeClient(ctx context.Context, r *InitializeRequest) (*Empty, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.Policy == nil || r.Usage == nil {
		return nil, status.Error(codes.InvalidArgument, "policy and initial usage are required")
	}
	u := clientpolicy.Usage{RawUpload: r.Usage.RawUpload, RawDownload: r.Usage.RawDownload, BilledBytes: r.Usage.BilledBytes, Remainder: r.Usage.Remainder}
	if err := s.engine.Initialize(runtimePolicy(r.Policy), u); err != nil {
		return nil, rpcError(err)
	}
	return &Empty{}, nil
}

func usage(u clientpolicy.Usage) *Usage {
	return &Usage{RawUpload: u.RawUpload, RawDownload: u.RawDownload, BilledBytes: u.BilledBytes, Remainder: u.Remainder}
}

func (s *service) GetClient(ctx context.Context, r *ClientRequest) (*ClientState, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	p, snap, err := s.engine.GetClient(r.GetClientId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &ClientState{FirstUsedAt: snap.FirstUsedAt, Policy: policyConfig(p), Usage: usage(snap.Usage), UncertainBytes: snap.UncertainBytes, Reasons: uint32(snap.Reasons), ActiveSessions: uint32(snap.ActiveSessions)}, nil
}

func (s *service) ApplyPolicies(ctx context.Context, r *ApplyRequest) (*Empty, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if r == nil || len(r.Policies) == 0 || len(r.Policies) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "provide 1 to 1000 policies")
	}
	policies := make([]clientpolicy.Policy, 0, len(r.Policies))
	for _, p := range r.Policies {
		if p == nil {
			return nil, status.Error(codes.InvalidArgument, "nil client policy")
		}
		policies = append(policies, runtimePolicy(p))
	}
	if err := s.engine.ApplyBatch(policies); err != nil {
		return nil, rpcError(err)
	}
	return &Empty{}, nil
}

func (s *service) RevokeClient(ctx context.Context, r *ClientRequest) (*Empty, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if r.GetExpectedPolicyVersion() == 0 {
		return nil, status.Error(codes.InvalidArgument, "expected policy version is required")
	}
	if err := s.engine.RemoveVersion(r.GetClientId(), r.GetExpectedPolicyVersion()); err != nil {
		return nil, rpcError(err)
	}
	return &Empty{}, nil
}

func (s *service) ListConnections(ctx context.Context, r *ClientRequest) (*Connections, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	connections, err := s.engine.Connections(r.GetClientId())
	if err != nil {
		return nil, rpcError(err)
	}
	out := &Connections{}
	for _, c := range connections {
		out.Connections = append(out.Connections, &Connection{SessionId: c.SessionID, InboundTag: c.InboundTag, AuthenticatedAccount: c.AuthenticatedAccount, OriginalTarget: c.OriginalTarget, ActualTarget: c.ActualTarget, PolicyVersion: c.PolicyVersion})
	}
	return out, nil
}

func (s *service) CloseConnections(ctx context.Context, r *ClientRequest) (*CloseResult, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	var n int
	var err error
	if r.GetInboundTag() == "" {
		n, err = s.engine.CloseConnections(r.GetClientId())
	} else {
		n, err = s.engine.CloseInboundConnections(r.GetClientId(), r.GetInboundTag())
	}
	if err != nil {
		return nil, rpcError(err)
	}
	return &CloseResult{Closed: uint32(n)}, nil
}

func (s *service) CheckpointUsage(ctx context.Context, _ *Empty) (*Empty, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if err := s.engine.Checkpoint(); err != nil {
		return nil, rpcError(err)
	}
	return &Empty{}, nil
}

func (s *service) ReadLedger(ctx context.Context, r *LedgerRequest) (*LedgerPage, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	records, err := s.engine.ReadLedger(r.GetAfterSequence(), int(r.GetLimit()))
	if err != nil {
		return nil, rpcError(err)
	}
	out := &LedgerPage{NextSequence: r.GetAfterSequence()}
	for _, v := range records {
		out.Records = append(out.Records, &LedgerRecord{FirstUsedAt: v.FirstUsedAt, InstanceId: v.InstanceID, Epoch: v.Epoch, Sequence: v.Sequence, ClientId: v.ClientID, PolicyVersion: v.PolicyVersion, Usage: usage(v.Usage), UncertainBytes: v.UncertainBytes, ReservedBytes: v.ReservedBytes, Revoked: v.Revoked})
		out.NextSequence = v.Sequence
	}
	return out, nil
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, _ interface{}) (interface{}, error) {
		s := new(service)
		if err := core.RequireFeatures(ctx, func(engine clientpolicy.Manager) { s.engine = engine }); err != nil {
			return nil, err
		}
		return s, nil
	}))
}
