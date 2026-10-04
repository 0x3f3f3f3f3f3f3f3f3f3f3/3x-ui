package runtime

import (
	"context"
	"slices"
)

func (r *Remote) EnrollClientMapping(ctx context.Context, request NodeClientMappingRequest) (*NodeClientMappingResult, error) {
	if request.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	var result NodeClientMappingResult
	if err := r.controlAuthority(ctx, "enroll", request, &result); err != nil {
		return nil, err
	}
	if result.Validate(request) != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	return &result, nil
}

func (a *RemoteAuthorityAPI) EnrollClientMapping(ctx context.Context, request NodeClientMappingRequest) (*NodeClientMappingResult, error) {
	if a == nil || a.remote == nil || request.Binding != a.binding || !slices.Contains(a.capabilities.GetCapabilities(), "client-authority-history-v1") {
		return nil, ErrNodeAuthorityDiscovery
	}
	return a.remote.EnrollClientMapping(ctx, request)
}
