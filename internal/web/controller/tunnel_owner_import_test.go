package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestTunnelOwnerImportIgnoresSourcePanelIdentity(t *testing.T) {
	cleanup, err := testpg.IsolatePackage("tunnel_owner_import")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	newHostTestDB(t)
	a := &InboundController{}
	engine := gin.New()
	engine.POST("/import", func(c *gin.Context) {
		session.SetAPIAuthUser(c, &model.User{Id: 1})
		a.importInbound(c)
	})
	data := `{"ownerClientId":"7eb617c7-1945-4d02-a590-ab6012f410b5","protocol":"tunnel","port":24108,"enable":false,"settings":{"rewriteAddress":"127.0.0.1","rewritePort":9001,"allowedNetwork":"tcp","clients":[{"email":"portable-owner","enable":true,"subId":"portable-owner"}]},"clientStats":[{"email":"portable-owner","up":123,"down":456}]}`
	req := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(url.Values{"data": {data}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	var response hostEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !response.Success {
		t.Fatalf("portable owner import failed: %s %v", w.Body.String(), err)
	}
	var owner model.ClientRecord
	if err := database.GetDB().Where("email = ?", "portable-owner").First(&owner).Error; err != nil || owner.StableID == "7eb617c7-1945-4d02-a590-ab6012f410b5" {
		t.Fatalf("import did not create a destination identity: %+v %v", owner, err)
	}
	var traffic xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", owner.Email).First(&traffic).Error; err != nil || traffic.Up != 123 || traffic.Down != 456 {
		t.Fatalf("portable history lost: %+v %v", traffic, err)
	}
}
