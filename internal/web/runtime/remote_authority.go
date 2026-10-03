package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

func (r *Remote) DiscoverAuthority(ctx context.Context, request AuthorityDiscoveryRequest) (*NodeAuthorityDiscovery, error) {
	if ctx == nil || request.Validate() != nil || r == nil || r.node == nil || !r.node.Enable || r.node.Transitive || r.node.Scheme != "https" {
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
	env, err := r.doWithResponseLimit(ctx, http.MethodPost, "panel/api/server/clientPolicyAuthority", request, NodeAuthorityMessageLimit, true)
	if err != nil {
		return nil, err
	}
	var result NodeAuthorityDiscovery
	decoder := json.NewDecoder(bytes.NewReader(env.Obj))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || result.Validate(request) != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	return &result, nil
}
