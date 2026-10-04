package controller

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func TestManagedPolicyProductHTTPAuthenticationAndExplicitActivation(t *testing.T) {
	engine, _ := newAPIAuthTestEngine(t)
	NewManagedPolicyAPIController(engine.Group(""))
	t.Cleanup(func() { _ = service.StopManagedPolicyCoordinator(context.Background()) })
	for _, scope := range []string{model.ApiScopeAdmin, model.ApiScopeNodeSync, model.ApiScopeMonitor} {
		if err := database.GetDB().Create(&model.ApiToken{Name: "managed-" + scope, Token: crypto.HashTokenSHA256("managed-" + scope), Enabled: true, Scope: scope}).Error; err != nil {
			t.Fatal(err)
		}
	}
	parent := model.ClientRecord{Email: "managed-http-parent", Enable: true, TotalGB: 128}
	if err := database.GetDB().Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, scope, body string
		plaintext                       bool
		status                          int
	}{
		{name: "inactive", method: "GET", status: 200, scope: model.ApiScopeAdmin},
		{name: "anonymous", method: "GET", status: 401},
		{name: "node-sync-status", method: "GET", status: 403, scope: model.ApiScopeNodeSync},
		{name: "monitor-status", method: "GET", status: 403, scope: model.ApiScopeMonitor},
		{name: "plaintext", method: "POST", path: "/activate", scope: model.ApiScopeAdmin, body: `{}`, plaintext: true, status: 403},
		{name: "node-sync-activate", method: "POST", path: "/activate", scope: model.ApiScopeNodeSync, body: `{}`, status: 403},
		{name: "unknown-field", method: "POST", path: "/activate", scope: model.ApiScopeAdmin, body: `{"force":true}`, status: 400},
		{name: "accounts", method: "POST", path: "/accounts", scope: model.ApiScopeAdmin, body: `{"parentClientId":"` + parent.StableID + `","afterNode":"","limit":2}`, status: 200},
		{name: "contributions-anonymous", method: "POST", path: "/contributions", body: `{}`, status: 401},
		{name: "contributions-node-sync", method: "POST", path: "/contributions", scope: model.ApiScopeNodeSync, body: `{}`, status: 403},
		{name: "contributions-monitor", method: "POST", path: "/contributions", scope: model.ApiScopeMonitor, body: `{}`, status: 403},
		{name: "contributions-plaintext", method: "POST", path: "/contributions", scope: model.ApiScopeAdmin, body: `{}`, plaintext: true, status: 403},
		{name: "contributions-invalid", method: "POST", path: "/contributions", scope: model.ApiScopeAdmin, body: `{}`, status: 400},
		{name: "contributions-duplicate", method: "POST", path: "/contributions", scope: model.ApiScopeAdmin, body: `{"limit":1,"limit":2}`, status: 400},
		{name: "contributions-bound", method: "POST", path: "/contributions", scope: model.ApiScopeAdmin, body: `{"parentClientId":"` + parent.StableID + `","clientId":"` + parent.StableID + `","limit":17}`, status: 400},
		{name: "accounts-node-sync", method: "POST", path: "/accounts", scope: model.ApiScopeNodeSync, body: `{}`, status: 403},
		{name: "accounts-duplicate", method: "POST", path: "/accounts", scope: model.ApiScopeAdmin, body: `{"limit":2,"limit":1}`, status: 400},
		{name: "accounts-bound", method: "POST", path: "/accounts", scope: model.ApiScopeAdmin, body: `{"parentClientId":"` + parent.StableID + `","afterNode":"","limit":129}`, status: 400},
		{name: "enroll-node-sync", method: "POST", path: "/enroll", scope: model.ApiScopeNodeSync, body: `{}`, status: 403},
		{name: "enroll-invalid", method: "POST", path: "/enroll", scope: model.ApiScopeAdmin, body: `{}`, status: 400},
		{name: "enroll-inactive", method: "POST", path: "/enroll", scope: model.ApiScopeAdmin, body: `{"inventoryId":1,"parentClientId":"` + parent.StableID + `","nodeId":"node-a","sourceId":"source-a","localClientId":"22222222-2222-4222-8222-222222222222","localPolicyVersion":"1"}`, status: 200},
		{name: "null", method: "POST", path: "/activate", scope: model.ApiScopeAdmin, body: `null`, status: 400},
		{name: "trailing", method: "POST", path: "/activate", scope: model.ApiScopeAdmin, body: `{} {}`, status: 400},
		{name: "oversize", method: "POST", path: "/activate", scope: model.ApiScopeAdmin, body: strings.Repeat(" ", panelruntime.NodeAuthorityMessageLimit+1), status: 413},
		{name: "activate", method: "POST", path: "/activate", scope: model.ApiScopeAdmin, body: `{}`, status: 200},
		{name: "active", method: "GET", scope: model.ApiScopeAdmin, status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := "https"
			if tc.plaintext {
				scheme = "http"
			}
			req := httptest.NewRequest(tc.method, scheme+"://panel.test/panel/api/server/clientPolicyCoordinator"+tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			if tc.scope != "" {
				req.Header.Set("Authorization", "Bearer managed-"+tc.scope)
			}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d", w.Code, tc.status)
			}
			if tc.status == 200 && tc.path != "/accounts" && tc.path != "/enroll" {
				active := tc.name != "inactive"
				want := `"active":false`
				if active {
					want = `"active":true`
				}
				if !strings.Contains(w.Body.String(), `"success":true`) || !strings.Contains(w.Body.String(), want) {
					t.Fatalf("missing original status: %s", w.Body.String())
				}
			}
			if tc.name == "enroll-inactive" && (!strings.Contains(w.Body.String(), `"success":false`) || !strings.Contains(w.Body.String(), errManagedPolicyUnavailable.Error())) {
				t.Fatal("inactive enrollment was acknowledged")
			}
			if tc.name == "accounts" && (!strings.Contains(w.Body.String(), `"pendingEnrollment":true`) || !strings.Contains(w.Body.String(), `"accounts":[]`)) {
				t.Fatal("inactive HTTP accounts fabricated budget")
			}
		})
	}
	t.Logf("managed policy product HTTP backend: %s", database.GetDB().Dialector.Name())
}

func TestManagedPolicyProductResponseIsBoundedBeforeWriting(t *testing.T) {
	engine := gin.New()
	engine.GET("/bounded", func(c *gin.Context) {
		writeManagedPolicyResult(c, map[string]string{"large": strings.Repeat("x", panelruntime.NodeAuthorityMessageLimit)}, nil)
	})
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/bounded", nil))
	if w.Body.Len() > panelruntime.NodeAuthorityMessageLimit || !strings.Contains(w.Body.String(), `"success":false`) {
		t.Fatal("oversize partial success escaped product envelope")
	}
}
