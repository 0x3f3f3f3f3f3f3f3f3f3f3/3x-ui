package controller

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

func TestNodeDelegationHTTPAuthenticationAndBounds(t *testing.T) {
	engine, _ := newAPIAuthTestEngine(t)
	NewNodeAuthorityAPIController(engine.Group(""))
	for _, scope := range []string{model.ApiScopeAdmin, model.ApiScopeNodeSync, model.ApiScopeMonitor} {
		if err := database.GetDB().Create(&model.ApiToken{Name: "delegation-" + scope, Token: crypto.HashTokenSHA256("delegation-" + scope), Enabled: true, Scope: scope}).Error; err != nil {
			t.Fatal(err)
		}
	}
	valid := []byte(`{"authorityId":"coordinator","generation":1,"nodeId":"node-a"}`)
	var instance string
	large := []byte(`{"authorityId":"` + strings.Repeat("x", panelruntime.NodeAuthorityMessageLimit) + `","generation":1,"nodeId":"node-a"}`)
	for _, tc := range []struct {
		name, scope, scheme, method string
		body                        []byte
		compressed, certificate     bool
		status                      int
	}{
		{name: "admin-tls", scope: model.ApiScopeAdmin, status: 200},
		{name: "node-sync-tls", scope: model.ApiScopeNodeSync, status: 200},
		{name: "verified-client-certificate", certificate: true, status: 200},
		{name: "missing-authentication", status: 401},
		{name: "monitor-denied", scope: model.ApiScopeMonitor, status: 403},
		{name: "plaintext-denied", scope: model.ApiScopeAdmin, scheme: "http", status: 403},
		{name: "wrong-method", scope: model.ApiScopeAdmin, method: http.MethodGet, status: 404},
		{name: "missing-field", scope: model.ApiScopeAdmin, body: []byte(`{"authorityId":"coordinator","generation":1}`), status: 400},
		{name: "unknown-field", scope: model.ApiScopeAdmin, body: []byte(`{"authorityId":"coordinator","generation":1,"nodeId":"node-a","extra":1}`), status: 400},
		{name: "duplicate-authority", scope: model.ApiScopeAdmin, body: []byte(`{"authorityId":"old","authorityId":"coordinator","generation":1,"nodeId":"node-a"}`), status: 400},
		{name: "escaped-duplicate", scope: model.ApiScopeAdmin, body: []byte(`{"authorityId":"coordinator","generation":1,"nodeId":"old","\u006eodeId":"node-a"}`), status: 400},
		{name: "case-alias", scope: model.ApiScopeAdmin, body: []byte(`{"AuthorityId":"coordinator","generation":1,"nodeId":"node-a"}`), status: 400},
		{name: "root-null", scope: model.ApiScopeAdmin, body: []byte(`null`), status: 400},
		{name: "null-field", scope: model.ApiScopeAdmin, body: []byte(`{"authorityId":null,"generation":1,"nodeId":"node-a"}`), status: 400},
		{name: "string-generation", scope: model.ApiScopeAdmin, body: []byte(`{"authorityId":"coordinator","generation":"1","nodeId":"node-a"}`), status: 400},
		{name: "zero-generation", scope: model.ApiScopeAdmin, body: []byte(`{"authorityId":"coordinator","generation":0,"nodeId":"node-a"}`), status: 400},
		{name: "overflow-generation", scope: model.ApiScopeAdmin, body: []byte(`{"authorityId":"coordinator","generation":9223372036854775808,"nodeId":"node-a"}`), status: 400},
		{name: "trailing-json", scope: model.ApiScopeAdmin, body: append(append([]byte{}, valid...), []byte(` {}`)...), status: 400},
		{name: "compressed-valid", scope: model.ApiScopeNodeSync, compressed: true, status: 200},
		{name: "compressed-duplicate", scope: model.ApiScopeAdmin, body: []byte(`{"authorityId":"old","authorityId":"coordinator","generation":1,"nodeId":"node-a"}`), compressed: true, status: 400},
		{name: "oversize-wire", scope: model.ApiScopeAdmin, body: large, status: 413},
		{name: "oversize-decoded", scope: model.ApiScopeAdmin, body: large, compressed: true, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme, method, body := tc.scheme, tc.method, tc.body
			if scheme == "" {
				scheme = "https"
			}
			if method == "" {
				method = http.MethodPost
			}
			if body == nil {
				body = valid
			}
			wire := body
			if tc.compressed {
				wire = wirecodec.Compress(body)
			}
			req := httptest.NewRequest(method, scheme+"://node.example/panel/api/server/clientPolicyDelegation", bytes.NewReader(wire))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			if tc.scope != "" {
				req.Header.Set("Authorization", "Bearer delegation-"+tc.scope)
			}
			if tc.compressed {
				req.Header.Set("Content-Encoding", wirecodec.EncodingZstd)
				req.Header.Set(wirecodec.HashHeader, wirecodec.Sha256Hex(body))
			}
			if tc.certificate {
				req.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
			}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d", w.Code, tc.status)
			}
			if tc.status == 200 {
				var envelope struct {
					Success bool                               `json:"success"`
					Obj     *panelruntime.NodeDelegationResult `json:"obj"`
				}
				if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || !envelope.Success || envelope.Obj == nil || envelope.Obj.InstanceID == "" || envelope.Obj.Role != (panelruntime.NodeDelegationRequest{AuthorityID: "coordinator", Generation: 1, NodeID: "node-a"}).Role() {
					t.Fatalf("fresh stopped setup did not persist the exact role: %s", w.Body.String())
				}
				if instance != "" && instance != envelope.Obj.InstanceID {
					t.Fatal("authorized configuration retry replaced the original instance")
				}
				instance = envelope.Obj.InstanceID
			}
		})
	}
	engine.GET("/delegation-test-login", func(c *gin.Context) {
		var user model.User
		if err := database.GetDB().First(&user).Error; err != nil {
			t.Fatal(err)
		}
		if err := session.SetLoginUser(c, &user); err != nil {
			t.Fatal(err)
		}
		token, err := session.EnsureCSRFToken(c)
		if err != nil {
			t.Fatal(err)
		}
		c.String(http.StatusOK, token)
	})
	login := httptest.NewRecorder()
	engine.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "https://node.example/delegation-test-login", nil))
	if login.Code != 200 {
		t.Fatalf("session login status=%d", login.Code)
	}
	// Session login and CSRF creation each save the same cookie. A browser
	// retains the last value; sending both would replay the pre-CSRF cookie.
	cookies := make(map[string]*http.Cookie)
	for _, cookie := range login.Result().Cookies() {
		cookies[cookie.Name] = cookie
	}
	for _, withToken := range []bool{false, true} {
		t.Run(map[bool]string{false: "session-missing-csrf", true: "session-valid-csrf"}[withToken], func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://node.example/panel/api/server/clientPolicyDelegation", bytes.NewReader(valid))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			for _, cookie := range cookies {
				req.AddCookie(cookie)
			}
			if withToken {
				req.Header.Set("X-CSRF-Token", login.Body.String())
			}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			want := 403
			if withToken {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("CSRF status=%d want=%d", w.Code, want)
			}
		})
	}
}
