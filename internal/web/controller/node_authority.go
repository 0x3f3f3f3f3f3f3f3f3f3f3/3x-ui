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
}

func discoverNodeAuthority(c *gin.Context) {
	var request panelruntime.AuthorityDiscoveryRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		pureJsonMsg(c, http.StatusBadRequest, false, panelruntime.ErrNodeAuthorityDiscovery.Error())
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || request.Validate() != nil {
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
