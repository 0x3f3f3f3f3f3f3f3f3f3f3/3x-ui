package controller

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func NewNodeAuthorityAPIController(g *gin.RouterGroup) {
	auth := &APIController{}
	api := g.Group("/panel/api")
	api.Use(auth.checkAPIAuth, auth.enforceTokenScope)
	api.Use(func(c *gin.Context) {
		if c.Request.TLS == nil {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	api.Use(middleware.ConfigEnvelopeMiddlewareWithLimit(panelruntime.NodeAuthorityMessageLimit), middleware.CSRFMiddleware())
	api.POST("/server/clientPolicyAuthority", discoverNodeAuthority)
	api.POST("/server/clientPolicyDelegation", configureNodeDelegation)
	registerNodeAuthorityControls(api)
}

func configureNodeDelegation(c *gin.Context) {
	request, err := decodeNodeDelegationRequest(c.Request.Body)
	if err != nil {
		pureJsonMsg(c, http.StatusBadRequest, false, panelruntime.ErrNodeAuthorityDiscovery.Error())
		return
	}
	result, err := (&service.ClientPolicyNodeService{}).ConfigureDelegation(c.Request.Context(), request)
	if err != nil {
		jsonObj(c, nil, panelruntime.ErrNodeAuthorityDiscovery)
		return
	}
	jsonObj(c, result, nil)
}

func decodeNodeDelegationRequest(body io.Reader) (panelruntime.NodeDelegationRequest, error) {
	var request panelruntime.NodeDelegationRequest
	fields, err := panelruntime.DecodeNodeAuthorityObject(body, "authorityId", "generation", "nodeId")
	if err != nil || len(fields) != 3 || json.Unmarshal(fields["authorityId"], &request.AuthorityID) != nil || json.Unmarshal(fields["generation"], &request.Generation) != nil || json.Unmarshal(fields["nodeId"], &request.NodeID) != nil || request.Validate() != nil {
		return request, panelruntime.ErrNodeAuthorityDiscovery
	}
	return request, nil
}

func discoverNodeAuthority(c *gin.Context) {
	request, err := decodeAuthorityDiscoveryRequest(c.Request.Body)
	if err != nil {
		pureJsonMsg(c, http.StatusBadRequest, false, panelruntime.ErrNodeAuthorityDiscovery.Error())
		return
	}
	result, err := (&service.ClientPolicyNodeService{}).DiscoverAuthority(c.Request.Context(), request)
	if err != nil {
		// Public discovery errors omit private socket/database diagnostics.
		jsonObj(c, nil, panelruntime.ErrNodeAuthorityDiscovery)
		return
	}
	jsonObj(c, result, nil)
}

// Token decoding preserves field identity: duplicate or case-alias fields must
// never overwrite an expected boot and turn a bound call into initial discovery.
func decodeAuthorityDiscoveryRequest(body io.Reader) (panelruntime.AuthorityDiscoveryRequest, error) {
	var request panelruntime.AuthorityDiscoveryRequest
	decoder := json.NewDecoder(body)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return request, panelruntime.ErrNodeAuthorityDiscovery
	}
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return request, panelruntime.ErrNodeAuthorityDiscovery
		}
		var target *string
		switch key {
		case "expectedInstanceId":
			target = &request.ExpectedInstanceID
		case "expectedBootId":
			target = &request.ExpectedBootID
		default:
			return request, panelruntime.ErrNodeAuthorityDiscovery
		}
		seen[key] = true
		token, err = decoder.Token()
		value, ok := token.(string)
		if err != nil || !ok {
			return request, panelruntime.ErrNodeAuthorityDiscovery
		}
		*target = value
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return request, panelruntime.ErrNodeAuthorityDiscovery
	}
	if _, err := decoder.Token(); err != io.EOF || request.Validate() != nil {
		return request, panelruntime.ErrNodeAuthorityDiscovery
	}
	return request, nil
}
