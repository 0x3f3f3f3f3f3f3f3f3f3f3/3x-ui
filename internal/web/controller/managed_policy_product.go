package controller

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

var errManagedPolicyUnavailable = errors.New("client policy management unavailable")

// Register separately so compressed and decoded management requests share the
// same bound. Existing scope middleware reserves these operations for admins.
func NewManagedPolicyAPIController(g *gin.RouterGroup) {
	auth := &APIController{}
	api := g.Group("/panel/api/server/clientPolicyCoordinator")
	api.Use(auth.checkAPIAuth, auth.enforceTokenScope)
	api.Use(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.TLS == nil {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	api.Use(middleware.ConfigEnvelopeMiddlewareWithLimit(panelruntime.NodeAuthorityMessageLimit), middleware.CSRFMiddleware())
	api.GET("", managedPolicyStatus)
	api.POST("/activate", activateManagedPolicy)
	api.POST("/accounts", managedPolicyAccounts)
	api.POST("/enroll", enrollManagedPolicy)
}

func enrollManagedPolicy(c *gin.Context) {
	fields, err := panelruntime.DecodeNodeAuthorityObject(c.Request.Body, "inventoryId", "parentClientId", "nodeId", "sourceId", "localClientId", "localPolicyVersion")
	var request service.ManagedPolicyEnrollmentRequest
	raw, encodeErr := json.Marshal(fields)
	if err != nil || encodeErr != nil || len(fields) != 6 || json.Unmarshal(raw, &request) != nil || request.Validate() != nil {
		pureJsonMsg(c, http.StatusBadRequest, false, errManagedPolicyUnavailable.Error())
		return
	}
	result, err := (&service.ManagedPolicyCoordinatorService{}).Enroll(c.Request.Context(), request)
	writeManagedPolicyResult(c, result, err)
}

func managedPolicyAccounts(c *gin.Context) {
	fields, err := panelruntime.DecodeNodeAuthorityObject(c.Request.Body, "parentClientId", "afterNode", "limit")
	var request service.ManagedPolicyAccountPageRequest
	if err != nil || json.Unmarshal(fields["parentClientId"], &request.ParentClientID) != nil || json.Unmarshal(fields["limit"], &request.Limit) != nil {
		pureJsonMsg(c, http.StatusBadRequest, false, errManagedPolicyUnavailable.Error())
		return
	}
	if raw, exists := fields["afterNode"]; exists && json.Unmarshal(raw, &request.AfterNode) != nil || request.Validate() != nil {
		pureJsonMsg(c, http.StatusBadRequest, false, errManagedPolicyUnavailable.Error())
		return
	}
	result, err := (&service.ManagedPolicyCoordinatorService{}).Accounts(c.Request.Context(), request)
	writeManagedPolicyResult(c, result, err)
}

func managedPolicyStatus(c *gin.Context) {
	result, err := (&service.ManagedPolicyCoordinatorService{}).Status(c.Request.Context())
	writeManagedPolicyResult(c, result, err)
}

func activateManagedPolicy(c *gin.Context) {
	fields, err := panelruntime.DecodeNodeAuthorityObject(c.Request.Body)
	if err != nil || len(fields) != 0 {
		pureJsonMsg(c, http.StatusBadRequest, false, errManagedPolicyUnavailable.Error())
		return
	}
	result, err := (&service.ManagedPolicyCoordinatorService{}).Activate(c.Request.Context())
	writeManagedPolicyResult(c, result, err)
}

func writeManagedPolicyResult(c *gin.Context, result any, err error) {
	if err != nil {
		jsonObj(c, nil, errManagedPolicyUnavailable)
		return
	}
	raw, encodeErr := json.Marshal(entity.Msg{Success: true, Obj: result})
	if encodeErr != nil || len(raw) > panelruntime.NodeAuthorityMessageLimit {
		jsonObj(c, nil, errManagedPolicyUnavailable)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}
