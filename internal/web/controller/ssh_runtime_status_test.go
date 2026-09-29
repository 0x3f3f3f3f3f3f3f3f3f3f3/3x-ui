package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

func TestSSHRuntimeStatusHTTPAuthorizationAndOwnerIsolation(t *testing.T) {
	testManagedRuntimeStatusHTTP(t, model.SSH, "/panel/api/inbounds/ssh/status", "authenticatedConnections")
}

func TestMieruRuntimeStatusHTTPAuthorizationAndOwnerIsolation(t *testing.T) {
	testManagedRuntimeStatusHTTP(t, model.Mieru, "/panel/api/inbounds/mieru/status", "authenticatedSessions")
}

func testManagedRuntimeStatusHTTP(t *testing.T, protocol model.Protocol, path, countField string) {
	t.Helper()
	authEngine, auth := newAPIAuthTestEngine(t)
	engine := gin.New()
	engine.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("ssh-status-test-secret"))))
	NewInboundController(engine.Group("/panel/api/inbounds", auth.checkAPIAuth, auth.enforceTokenScope))
	owner, err := auth.userService.GetFirstUser()
	if err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	other := model.User{Username: "status-other-owner"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	engine.GET("/test-login", func(c *gin.Context) {
		if err := session.SetLoginUser(c, &other); err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
	})
	rows := []*model.Inbound{
		{UserId: owner.Id, Protocol: protocol, Enable: true, Tag: "status-owner", Settings: `{"hostKey":"PRIVATE KEY must not be read","password":"private-native-password"}`},
		{UserId: other.Id, Protocol: protocol, Enable: true, Tag: "status-other"},
		{UserId: owner.Id, Protocol: model.VLESS, Enable: true, Tag: "status-native"},
	}
	for _, inbound := range rows {
		if err := db.Create(inbound).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []string{model.ApiScopeAdmin, model.ApiScopeMonitor, model.ApiScopeNodeSync} {
		if err := db.Create(&model.ApiToken{Name: scope, Token: crypto.HashTokenSHA256(scope), Enabled: true, Scope: scope}).Error; err != nil {
			t.Fatal(err)
		}
	}
	request := func(path, token string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for _, value := range cookies {
			req.AddCookie(value)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	if w := request(path, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous request status = %d, want 401: %s", w.Code, w.Body.String())
	}
	for _, scope := range []string{model.ApiScopeMonitor, model.ApiScopeNodeSync} {
		probe := httptest.NewRequest(http.MethodGet, "/panel/api/server/status", nil)
		probe.Header.Set("Authorization", "Bearer "+scope)
		w := httptest.NewRecorder()
		authEngine.ServeHTTP(w, probe)
		if w.Code != http.StatusOK {
			t.Fatalf("valid %s token rejected by its allowed status endpoint: %d", scope, w.Code)
		}
		if w := request(path, scope, nil); w.Code != http.StatusForbidden {
			t.Fatalf("%s status request = %d, want 403: %s", scope, w.Code, w.Body.String())
		}
	}
	assertOwner := func(w *httptest.ResponseRecorder, inboundID int) {
		t.Helper()
		var envelope struct {
			Success bool             `json:"success"`
			Obj     []map[string]any `json:"obj"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || !envelope.Success || len(envelope.Obj) != 1 {
			t.Fatalf("owner status response = %d %s", w.Code, w.Body.String())
		}
		status := envelope.Obj[0]
		if len(status) != 4 || status["inboundId"] != float64(inboundID) || status["state"] != "idle" || status["reason"] != "no enabled clients" || status[countField] != float64(0) {
			t.Fatalf("wrong owner or unexpected status fields: %s", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), "private-native-password") {
			t.Fatal("status response exposed stored credentials")
		}
	}
	assertOwner(request(path, model.ApiScopeAdmin, nil), rows[0].Id)
	login := request("/test-login", "", nil)
	if login.Code != http.StatusOK {
		t.Fatalf("session login failed: %d", login.Code)
	}
	assertOwner(request(path, "", login.Result().Cookies()), rows[1].Id)
	if err := db.Migrator().DropTable(&model.Inbound{}); err != nil {
		t.Fatal(err)
	}
	failed := request(path, model.ApiScopeAdmin, nil)
	var failure struct {
		Success bool            `json:"success"`
		Msg     string          `json:"msg"`
		Obj     json.RawMessage `json:"obj"`
	}
	if err := json.Unmarshal(failed.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failed.Code != http.StatusOK || failure.Success || !strings.Contains(failure.Msg, "inbounds") || string(failure.Obj) != "null" {
		t.Fatalf("failed database query returned a successful status view: %d %s", failed.Code, failed.Body.String())
	}
}
