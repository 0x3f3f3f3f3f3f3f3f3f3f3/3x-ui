package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func registerNodeClientMapping(api *gin.RouterGroup) {
	node := &service.ClientPolicyNodeService{}
	api.POST("/server/clientPolicyAuthority/enroll", func(c *gin.Context) {
		handleNodeAuthorityControl(c, node.EnrollClientMapping)
	})
}
