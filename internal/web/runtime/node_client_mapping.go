package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"math"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/protobuf/proto"
)

type NodeClientMappingRequest struct {
	Binding              NodeAuthorityControlBinding `json:"binding"`
	GlobalClientID       string                      `json:"globalClientId"`
	LocalClientID        string                      `json:"localClientId"`
	GlobalPolicyVersion  uint64                      `json:"globalPolicyVersion,string"`
	LocalPolicyVersion   uint64                      `json:"localPolicyVersion,string"`
	ExpectedPolicyDigest string                      `json:"expectedPolicyDigest"`
}

type NodeClientMappingResult struct {
	NodeAuthorityControlIdentity
	Mapping policyauthority.ClientMapping `json:"mapping"`
}

func (r NodeClientMappingRequest) Validate() error {
	if r.Binding.Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	m := policyauthority.ClientMapping{Authority: policyauthority.Identity{AuthorityID: r.Binding.AuthorityID, Generation: r.Binding.Generation}, NodeAnchor: policyauthority.Identity{AuthorityID: "syntax-only", Generation: 1}, NodeID: r.Binding.NodeID, SourceID: r.Binding.ExpectedInstanceID, GlobalClientID: r.GlobalClientID, LocalClientID: r.LocalClientID, GlobalPolicyVersion: r.GlobalPolicyVersion, LocalPolicyVersion: r.LocalPolicyVersion, PolicyDigest: r.ExpectedPolicyDigest}
	if m.Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func (r *NodeClientMappingResult) Validate(request NodeClientMappingRequest) error {
	if r == nil || request.Validate() != nil || r.NodeAuthorityControlIdentity.Validate(request.Binding) != nil || r.Mapping.Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	m := r.Mapping
	if m.Authority.AuthorityID != request.Binding.AuthorityID || m.Authority.Generation != request.Binding.Generation || m.NodeID != request.Binding.NodeID || m.SourceID != request.Binding.ExpectedInstanceID || m.GlobalClientID != request.GlobalClientID || m.LocalClientID != request.LocalClientID || m.GlobalPolicyVersion != request.GlobalPolicyVersion || m.LocalPolicyVersion != request.LocalPolicyVersion || m.PolicyDigest != request.ExpectedPolicyDigest {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func EffectiveClientPolicyDigest(policy *clientpolicy.PolicyConfig) (string, error) {
	if policy == nil || policy.Version > math.MaxInt64 || policy.QuotaBytes > math.MaxInt64 || len(policy.ProtoReflect().GetUnknown()) != 0 {
		return "", ErrNodeAuthorityDiscovery
	}
	effective := clientpolicy.Policy{ClientID: policy.ClientId, Version: policy.Version, Enabled: policy.Enabled, Multiplier: policy.MultiplierMicros, QuotaBytes: policy.QuotaBytes, UploadRate: policy.UploadBytesPerSecond, DownloadRate: policy.DownloadBytesPerSecond, BurstBytes: policy.BurstBytes, ExpiresAt: policy.ExpiresAt, QuotaBaselineBytes: policy.QuotaBaselineBytes, QuotaBaselineRemainder: policy.QuotaBaselineRemainder}
	if effective.Validate() != nil {
		return "", ErrNodeAuthorityDiscovery
	}
	copy := proto.Clone(policy).(*clientpolicy.PolicyConfig)
	copy.ClientId, copy.Version = "", 0
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(copy)
	if err != nil {
		return "", ErrNodeAuthorityDiscovery
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
