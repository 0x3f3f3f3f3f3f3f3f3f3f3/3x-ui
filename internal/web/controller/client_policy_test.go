package controller

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyHTTPAuthorizationAndExactAccounting(t *testing.T) {
	_, auth := newAPIAuthTestEngine(t)
	engine := gin.New()
	engine.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("policy-api-test-secret"))))
	group := engine.Group("/panel/api/clients", auth.checkAPIAuth, auth.enforceTokenScope)
	NewClientController(group)
	db := database.GetDB()
	client := model.ClientRecord{Email: "policy-api", Enable: true}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{Protocol: model.SSH, Settings: "{}"}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: client.Email, Enable: true, Up: 9007199254740993, Down: 7}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.NewClientUsageLedger(db).Ensure(context.Background(), client.PolicyID); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{model.ApiScopeAdmin, model.ApiScopeMonitor, model.ApiScopeNodeSync} {
		if err := db.Create(&model.ApiToken{Name: scope, Token: crypto.HashTokenSHA256(scope), Enabled: true, Scope: scope}).Error; err != nil {
			t.Fatal(err)
		}
	}
	request := func(method, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/panel/api/clients/policy/"+client.Email, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	payload := fmt.Sprintf(`{"policyId":%q,"version":0,"uploadBps":65536,"downloadBps":131072,"multiplier":"1.5","scope":"local"}`, client.PolicyID)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if w := request(method, "", payload); w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s reached policy endpoint: %d %s", method, w.Code, w.Body.String())
		}
		for _, scope := range []string{model.ApiScopeMonitor, model.ApiScopeNodeSync} {
			if w := request(method, scope, payload); w.Code != http.StatusForbidden {
				t.Fatalf("%s %s reached policy endpoint: %d %s", scope, method, w.Code, w.Body.String())
			}
		}
	}
	decode := func(w *httptest.ResponseRecorder, success bool) service.ClientPolicy {
		t.Helper()
		var envelope struct {
			Success bool                 `json:"success"`
			Msg     string               `json:"msg"`
			Obj     service.ClientPolicy `json:"obj"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || envelope.Success != success {
			t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
		}
		return envelope.Obj
	}
	before := decode(request(http.MethodGet, model.ApiScopeAdmin, ""), true)
	if before.Version != 0 || before.Usage.Up != "9007199254740993" || before.Usage.Billed != "9007199254741000" {
		t.Fatalf("API rounded bytes or changed policy during denied request: %+v", before)
	}
	after := decode(request(http.MethodPost, model.ApiScopeAdmin, payload), true)
	if after.Version != 1 || after.Multiplier != "1.5" || after.UploadBps != 65536 || after.Usage.Billed != before.Usage.Billed {
		t.Fatalf("HTTP edit lost fields or repriced history: %+v", after)
	}
	stale := request(http.MethodPost, model.ApiScopeAdmin, payload)
	decode(stale, false)
	if !strings.Contains(stale.Body.String(), service.ErrClientPolicyConflict.Error()) {
		t.Fatalf("missing version conflict: %s", stale.Body.String())
	}
	decode(request(http.MethodPost, model.ApiScopeAdmin, `{"version":1,"multiplier":1.5,"scope":"local"}`), false)
	final := decode(request(http.MethodGet, model.ApiScopeAdmin, ""), true)
	if final.Version != 1 || final.Multiplier != "1.5" || final.DownloadBps != 131072 {
		t.Fatalf("rejected HTTP edit partially wrote policy: %+v", final)
	}
}
