package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

func (r *Remote) ConfigureDelegation(ctx context.Context, request NodeDelegationRequest) (*NodeDelegationResult, error) {
	if ctx == nil || request.Validate() != nil || r == nil || r.node == nil || !r.node.Enable || r.node.Transitive || r.node.Scheme != "" && r.node.Scheme != "https" {
		return nil, ErrNodeAuthorityDiscovery
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch r.node.TlsVerifyMode {
	case "", "verify", "pin", "mtls":
	default:
		return nil, ErrNodeAuthorityDiscovery
	}
	env, err := r.doWithResponseLimit(ctx, http.MethodPost, "panel/api/server/clientPolicyDelegation", request, NodeAuthorityMessageLimit, true)
	if err != nil {
		return nil, err
	}
	fields, err := DecodeNodeAuthorityObject(bytes.NewReader(env.Obj), "instanceId", "role")
	if err != nil || len(fields) != 2 {
		return nil, ErrNodeAuthorityDiscovery
	}
	role, err := DecodeNodeAuthorityObject(bytes.NewReader(fields["role"]), "mode", "authorityId", "generation", "nodeId")
	if err != nil || len(role) != 4 {
		return nil, ErrNodeAuthorityDiscovery
	}
	var result NodeDelegationResult
	if json.Unmarshal(env.Obj, &result) != nil || !authorityInstance(result.InstanceID) || result.Role != request.Role() || result.Role.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	return &result, nil
}
