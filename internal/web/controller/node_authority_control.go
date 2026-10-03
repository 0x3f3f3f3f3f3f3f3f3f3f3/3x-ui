package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func registerNodeAuthorityControls(api *gin.RouterGroup) {
	node := &service.ClientPolicyNodeService{}
	api.POST("/server/clientPolicyAuthority/requests", func(c *gin.Context) { handleNodeAuthorityControl(c, node.ReadAuthorityRequests) })
	api.POST("/server/clientPolicyAuthority/install", func(c *gin.Context) { handleNodeAuthorityControl(c, node.InstallAuthorityGrant) })
	api.POST("/server/clientPolicyAuthority/get", func(c *gin.Context) { handleNodeAuthorityControl(c, node.GetAuthorityGrant) })
	api.POST("/server/clientPolicyAuthority/pause", func(c *gin.Context) { handleNodeAuthorityControl(c, node.PauseAuthorityGrant) })
	api.POST("/server/clientPolicyAuthority/seal", func(c *gin.Context) { handleNodeAuthorityControl(c, node.SealAuthorityGrant) })
	api.POST("/server/clientPolicyAuthority/renew", func(c *gin.Context) { handleNodeAuthorityControl(c, node.RenewAuthorityGrant) })
}

func handleNodeAuthorityControl[Request, Result any](c *gin.Context, operation func(context.Context, Request) (*Result, error)) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, panelruntime.NodeAuthorityMessageLimit+1))
	var request Request
	if err != nil || len(raw) > panelruntime.NodeAuthorityMessageLimit || json.Unmarshal(raw, &request) != nil {
		pureJsonMsg(c, http.StatusBadRequest, false, panelruntime.ErrNodeAuthorityDiscovery.Error())
		return
	}
	result, err := operation(c.Request.Context(), request)
	writeNodeAuthorityControl(c, result, err)
}

// Encode the complete bounded envelope before writing any response. A failed
// mutation acknowledgement remains an error and never releases an allocation.
func writeNodeAuthorityControl(c *gin.Context, result any, err error) {
	if err != nil {
		jsonObj(c, nil, panelruntime.ErrNodeAuthorityDiscovery)
		return
	}
	raw, encodeErr := json.Marshal(entity.Msg{Success: true, Obj: result})
	if encodeErr != nil || len(raw) > panelruntime.NodeAuthorityMessageLimit {
		jsonObj(c, nil, panelruntime.ErrNodeAuthorityDiscovery)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}
