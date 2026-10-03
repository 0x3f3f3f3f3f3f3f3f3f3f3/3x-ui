package runtime

import (
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"math"
)

type NodeAuthorityControlBinding struct {
	ExpectedInstanceID string `json:"expectedInstanceId"`
	ExpectedBootID     string `json:"expectedBootId"`
	AuthorityID        string `json:"authorityId"`
	Generation         uint64 `json:"generation"`
	NodeID             string `json:"nodeId"`
}

type NodeAuthorityRequestsRequest struct {
	Binding NodeAuthorityControlBinding `json:"binding"`
	Limit   uint32                      `json:"limit"`
}

type NodeAuthorityInstallRequest struct {
	Binding NodeAuthorityControlBinding `json:"binding"`
	Grant   *command.ExecutionGrant     `json:"grant"`
}

type NodeAuthorityGrantRequest struct {
	Binding  NodeAuthorityControlBinding `json:"binding"`
	ClientID string                      `json:"clientId"`
	GrantID  string                      `json:"grantId"`
}

type NodeAuthorityRenewalRequest struct {
	Binding NodeAuthorityControlBinding      `json:"binding"`
	Renewal *command.AuthorityRenewalRequest `json:"renewal"`
}

type NodeAuthorityControlIdentity struct {
	InstanceID    string            `json:"instanceId"`
	BootID        string            `json:"bootId"`
	ExecutionRole NodeExecutionRole `json:"executionRole"`
}

type NodeAuthorityRequestsResult struct {
	NodeAuthorityControlIdentity
	Requests *command.AuthorityRequests `json:"requests"`
}

type NodeAuthorityGrantResult struct {
	NodeAuthorityControlIdentity
	State *command.ExecutionGrantState `json:"state"`
}

type NodeAuthorityRenewalResult struct {
	NodeAuthorityControlIdentity
	Renewal *command.AuthorityRenewalRequest `json:"renewal"`
}

func (b NodeAuthorityControlBinding) Role() NodeExecutionRole {
	return NodeExecutionRole{Mode: NodeExecutionDelegated, AuthorityID: b.AuthorityID, Generation: b.Generation, NodeID: b.NodeID}
}

func (b NodeAuthorityControlBinding) Validate() error {
	if !authorityInstance(b.ExpectedInstanceID) || !authorityNonce(b.ExpectedBootID) || b.Role().Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func (r NodeAuthorityRequestsRequest) Validate() error {
	if r.Binding.Validate() != nil || r.Limit == 0 || r.Limit > 128 {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func validNodeAuthorityShare(s *command.AuthorityShare) bool {
	if s == nil {
		return false
	}
	if s.Unlimited {
		return s.Rate == 0 && s.Burst == 0
	}
	return s.Rate <= 1<<40 && s.Burst <= 1<<20 && (s.Rate == 0 && s.Burst == 0 || s.Rate > 0 && s.Burst > 0)
}

func validNodeExecutionGrant(b NodeAuthorityControlBinding, g *command.ExecutionGrant) bool {
	return b.Validate() == nil && g != nil && g.Authority != nil && g.Authority.AuthorityId == b.AuthorityID && g.Authority.Generation == b.Generation && g.Authority.NodeId == b.NodeID && g.InstanceId == b.ExpectedInstanceID && g.BootId == b.ExpectedBootID && authorityInstance(g.ClientId) && authorityInstance(g.WindowId) && authorityInstance(g.GrantId) && authorityNonce(g.ChallengeId) && g.PolicyVersion > 0 && g.PolicyVersion <= math.MaxInt64 && g.Sequence > 0 && g.Sequence <= math.MaxInt64 && g.Capacity > 0 && g.Capacity <= math.MaxInt64 && validNodeAuthorityShare(g.Upload) && validNodeAuthorityShare(g.Download) && g.LeaseDurationMillis > 0 && g.LeaseDurationMillis <= 10000
}

func (r NodeAuthorityInstallRequest) Validate() error {
	if !validNodeExecutionGrant(r.Binding, r.Grant) {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func (r NodeAuthorityGrantRequest) Validate() error {
	if r.Binding.Validate() != nil || !authorityInstance(r.ClientID) || !authorityInstance(r.GrantID) {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func (r NodeAuthorityRenewalRequest) Validate() error {
	g := r.Renewal
	if r.Binding.Validate() != nil || g == nil || g.ExpectedBootId != r.Binding.ExpectedBootID || !authorityInstance(g.ClientId) || !authorityInstance(g.GrantId) || !authorityNonce(g.ChallengeId) || g.Sequence == 0 || g.Sequence > math.MaxInt64 || g.LeaseDurationMillis == 0 || g.LeaseDurationMillis > 10000 {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func (i NodeAuthorityControlIdentity) Validate(b NodeAuthorityControlBinding) error {
	if b.Validate() != nil || i.InstanceID != b.ExpectedInstanceID || i.BootID != b.ExpectedBootID || i.ExecutionRole != b.Role() {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func (r *NodeAuthorityRequestsResult) Validate(request NodeAuthorityRequestsRequest) error {
	if request.Validate() != nil || r == nil || r.NodeAuthorityControlIdentity.Validate(request.Binding) != nil || r.Requests == nil || r.Requests.InstanceId != r.InstanceID || r.Requests.BootId != r.BootID || len(r.Requests.Requests) > int(request.Limit) {
		return ErrNodeAuthorityDiscovery
	}
	clients, ids := make(map[string]bool), make(map[string]bool)
	for _, demand := range r.Requests.Requests {
		if demand == nil || !authorityInstance(demand.ClientId) || !authorityNonce(demand.RequestId) || demand.PolicyVersion == 0 || demand.PolicyVersion > math.MaxInt64 || demand.PreviousGrantId != "" && !authorityInstance(demand.PreviousGrantId) || clients[demand.ClientId] || ids[demand.RequestId] {
			return ErrNodeAuthorityDiscovery
		}
		clients[demand.ClientId], ids[demand.RequestId] = true, true
	}
	return nil
}

func (r *NodeAuthorityGrantResult) Validate(request NodeAuthorityGrantRequest) error {
	if request.Validate() != nil || r == nil || r.NodeAuthorityControlIdentity.Validate(request.Binding) != nil || r.State == nil || !validNodeExecutionGrant(request.Binding, r.State.Grant) || r.State.Grant.ClientId != request.ClientID || r.State.Grant.GrantId != request.GrantID || r.State.Sequence == 0 || r.State.Sequence > math.MaxInt64 || r.State.Usage == nil {
		return ErrNodeAuthorityDiscovery
	}
	u := r.State.Usage
	if u.RawUpload > math.MaxInt64 || u.RawDownload > math.MaxInt64 || u.BilledBytes > r.State.Grant.Capacity || u.Remainder >= 1000000 || u.BilledBytes == r.State.Grant.Capacity && u.Remainder != 0 {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}
