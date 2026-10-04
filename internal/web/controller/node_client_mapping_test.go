package controller

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"testing"
)

func TestNodeClientMappingHTTPAuthenticationAndBounds(t *testing.T) {
	testNodeAuthorityControlHTTPAuthenticationAndBounds(t, []string{"enroll"})
	t.Logf("node client mapping HTTP backend: %s", database.GetDB().Dialector.Name())
}
