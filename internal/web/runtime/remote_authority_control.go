package runtime

import (
	"context"
	"encoding/json"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"net/http"
)

func (r *Remote) validateAuthorityControl(ctx context.Context) error {
	if ctx == nil || r == nil || r.node == nil || !r.node.Enable || r.node.Transitive || r.node.Scheme != "" && r.node.Scheme != "https" {
		return ErrNodeAuthorityDiscovery
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch r.node.TlsVerifyMode {
	case "", "verify", "pin", "mtls":
		return nil
	default:
		return ErrNodeAuthorityDiscovery
	}
}

func (r *Remote) controlAuthority(ctx context.Context, path string, request any, result any) error {
	if err := r.validateAuthorityControl(ctx); err != nil {
		return err
	}
	envelope, err := r.doWithResponseLimit(ctx, http.MethodPost, "panel/api/server/clientPolicyAuthority/"+path, request, NodeAuthorityMessageLimit, true)
	if err != nil {
		return err
	}
	if json.Unmarshal(envelope.Obj, result) != nil {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func (r *Remote) ReadAuthorityRequests(ctx context.Context, request NodeAuthorityRequestsRequest) (*NodeAuthorityRequestsResult, error) {
	if request.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	var result NodeAuthorityRequestsResult
	if err := r.controlAuthority(ctx, "requests", request, &result); err != nil {
		return nil, err
	}
	if result.Validate(request) != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	return &result, nil
}
func (r *Remote) InstallAuthorityGrant(ctx context.Context, request NodeAuthorityInstallRequest) (*NodeAuthorityGrantResult, error) {
	if request.Grant != nil {
		request.Grant = proto.Clone(request.Grant).(*command.ExecutionGrant)
	}
	if request.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	var result NodeAuthorityGrantResult
	if err := r.controlAuthority(ctx, "install", request, &result); err != nil {
		return nil, err
	}
	if result.Validate(NodeAuthorityGrantRequest{Binding: request.Binding, ClientID: request.Grant.ClientId, GrantID: request.Grant.GrantId}) != nil || result.State.Sealed || !proto.Equal(result.State.Grant, request.Grant) {
		return nil, ErrNodeAuthorityDiscovery
	}
	return &result, nil
}
func (r *Remote) authorityGrantState(ctx context.Context, path string, request NodeAuthorityGrantRequest, sealed bool) (*NodeAuthorityGrantResult, error) {
	if request.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	var result NodeAuthorityGrantResult
	if err := r.controlAuthority(ctx, path, request, &result); err != nil {
		return nil, err
	}
	if result.Validate(request) != nil || sealed && !result.State.Sealed {
		return nil, ErrNodeAuthorityDiscovery
	}
	return &result, nil
}
func (r *Remote) GetAuthorityGrant(ctx context.Context, request NodeAuthorityGrantRequest) (*NodeAuthorityGrantResult, error) {
	return r.authorityGrantState(ctx, "get", request, false)
}
func (r *Remote) PauseAuthorityGrant(ctx context.Context, request NodeAuthorityGrantRequest) (*NodeAuthorityGrantResult, error) {
	return r.authorityGrantState(ctx, "pause", request, true)
}
func (r *Remote) SealAuthorityGrant(ctx context.Context, request NodeAuthorityGrantRequest) (*NodeAuthorityGrantResult, error) {
	return r.authorityGrantState(ctx, "seal", request, true)
}
func (r *Remote) RenewAuthorityGrant(ctx context.Context, request NodeAuthorityRenewalRequest) (*NodeAuthorityRenewalResult, error) {
	if request.Renewal != nil {
		request.Renewal = proto.Clone(request.Renewal).(*command.AuthorityRenewalRequest)
	}
	if request.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	var result NodeAuthorityRenewalResult
	if err := r.controlAuthority(ctx, "renew", request, &result); err != nil {
		return nil, err
	}
	if result.NodeAuthorityControlIdentity.Validate(request.Binding) != nil || !proto.Equal(result.Renewal, request.Renewal) {
		return nil, ErrNodeAuthorityDiscovery
	}
	return &result, nil
}
