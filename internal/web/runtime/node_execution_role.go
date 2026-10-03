package runtime

import "math"

const (
	NodeExecutionLocal     = "local"
	NodeExecutionDelegated = "delegated"
)

type NodeExecutionRole struct {
	Mode        string `json:"mode"`
	AuthorityID string `json:"authorityId,omitempty"`
	Generation  uint64 `json:"generation,omitempty"`
	NodeID      string `json:"nodeId,omitempty"`
}

func (r NodeExecutionRole) Validate() error {
	switch r.Mode {
	case NodeExecutionLocal:
		if r.AuthorityID == "" && r.Generation == 0 && r.NodeID == "" {
			return nil
		}
	case NodeExecutionDelegated:
		if authorityInstance(r.AuthorityID) && authorityInstance(r.NodeID) && r.Generation > 0 && r.Generation <= math.MaxInt64 {
			return nil
		}
	}
	return ErrNodeAuthorityDiscovery
}

type NodeDelegationRequest struct {
	AuthorityID string `json:"authorityId"`
	Generation  uint64 `json:"generation"`
	NodeID      string `json:"nodeId"`
}

func (r NodeDelegationRequest) Role() NodeExecutionRole {
	return NodeExecutionRole{Mode: NodeExecutionDelegated, AuthorityID: r.AuthorityID, Generation: r.Generation, NodeID: r.NodeID}
}

func (r NodeDelegationRequest) Validate() error { return r.Role().Validate() }

type NodeDelegationResult struct {
	InstanceID string            `json:"instanceId"`
	Role       NodeExecutionRole `json:"role"`
}
