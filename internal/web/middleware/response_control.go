package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const responseControlKey = "xui-response-controller"

// ResponseControlMiddleware must precede response wrappers such as gzip, whose
// writer does not expose Unwrap. Capturing the controller changes no deadlines.
func ResponseControlMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(responseControlKey, http.NewResponseController(c.Writer))
		c.Next()
	}
}

// SetResponseWriteDeadline lets an authenticated long operation extend only its
// own response, while retaining the server's ordinary request/write limits.
func SetResponseWriteDeadline(c *gin.Context, deadline time.Time) error {
	if value, ok := c.Get(responseControlKey); ok {
		if controller, ok := value.(*http.ResponseController); ok {
			return controller.SetWriteDeadline(deadline)
		}
	}
	return http.NewResponseController(c.Writer).SetWriteDeadline(deadline)
}
