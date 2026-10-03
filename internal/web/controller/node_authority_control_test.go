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

func TestNodeAuthorityControlHTTPAuthenticationAndBounds(t *testing.T) {
	engine, _ := newAPIAuthTestEngine(t)
	t.Logf("node authority control backend: %s", database.GetDB().Dialector.Name())
	NewNodeAuthorityAPIController(engine.Group(""))
	for _, scope := range []string{model.ApiScopeAdmin, model.ApiScopeNodeSync, model.ApiScopeMonitor} {
		if err := database.GetDB().Create(&model.ApiToken{Name: "control-" + scope, Token: crypto.HashTokenSHA256("control-" + scope), Enabled: true, Scope: scope}).Error; err != nil {
			t.Fatal(err)
		}
	}
	const binding = `{"expectedInstanceId":"node-instance","expectedBootId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","authorityId":"coordinator","generation":3,"nodeId":"node-a"}`
	const authority = `{"authorityId":"coordinator","generation":"3","nodeId":"node-a"}`
	const ids = `"instanceId":"node-instance","bootId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","clientId":"client-a","windowId":"window-a","grantId":"grant-a"`
	const share = `{"unlimited":true,"rate":"0","burst":"0"}`
	grant := `{"authority":` + authority + `,` + ids + `,"challengeId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","policyVersion":"1","sequence":"1","capacity":"9007199254740993","upload":` + share + `,"download":` + share + `,"leaseDurationMillis":"1000"}`
	renewal := `{"expectedBootId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","clientId":"client-a","grantId":"grant-a","challengeId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sequence":"1","leaseDurationMillis":"1000"}`
	bodies := map[string]string{
		"requests": `{"binding":` + binding + `,"limit":1}`,
		"install":  `{"binding":` + binding + `,"grant":` + grant + `}`,
		"get":      `{"binding":` + binding + `,"clientId":"client-a","grantId":"grant-a"}`,
		"pause":    `{"binding":` + binding + `,"clientId":"client-a","grantId":"grant-a"}`,
		"seal":     `{"binding":` + binding + `,"clientId":"client-a","grantId":"grant-a"}`,
		"renew":    `{"binding":` + binding + `,"renewal":` + renewal + `}`,
	}
	request := func(op, scheme, method, scope string, body []byte, compressed, certificate bool, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		wire := body
		if compressed {
			wire = wirecodec.Compress(body)
		}
		req := httptest.NewRequest(method, scheme+"://node.example/panel/api/server/clientPolicyAuthority/"+op, bytes.NewReader(wire))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if scope != "" {
			req.Header.Set("Authorization", "Bearer control-"+scope)
		}
		if compressed {
			req.Header.Set("Content-Encoding", wirecodec.EncodingZstd)
			req.Header.Set(wirecodec.HashHeader, wirecodec.Sha256Hex(body))
		}
		if certificate {
			req.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
		}
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	for _, op := range []string{"requests", "install", "get", "pause", "seal", "renew"} {
		body := []byte(bodies[op])
		if !json.Valid(body) {
			t.Fatal("invalid literal fixture", op)
		}
		for _, tc := range []struct {
			name, scope, scheme, method string
			certificate, compressed     bool
			status                      int
		}{
			{name: "admin", scope: model.ApiScopeAdmin, status: 200},
			{name: "node-sync", scope: model.ApiScopeNodeSync, status: 200},
			{name: "verified-certificate", certificate: true, status: 200},
			{name: "missing-auth", status: 401},
			{name: "monitor", scope: model.ApiScopeMonitor, status: 403},
			{name: "plaintext", scope: model.ApiScopeAdmin, scheme: "http", status: 403},
			{name: "wrong-method", scope: model.ApiScopeAdmin, method: http.MethodGet, status: 404},
			{name: "compressed", scope: model.ApiScopeNodeSync, compressed: true, status: 200},
		} {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				scheme, method := tc.scheme, tc.method
				if scheme == "" {
					scheme = "https"
				}
				if method == "" {
					method = http.MethodPost
				}
				w := request(op, scheme, method, tc.scope, body, tc.compressed, tc.certificate, nil, "")
				if w.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
				}
				if tc.status == 200 {
					var envelope struct {
						Success bool            `json:"success"`
						Obj     json.RawMessage `json:"obj"`
						Msg     string          `json:"msg"`
					}
					if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Success || string(envelope.Obj) != "null" || !strings.Contains(envelope.Msg, panelruntime.ErrNodeAuthorityDiscovery.Error()) {
						t.Fatalf("stopped owner must refuse opaquely: %s", w.Body.String())
					}
					if strings.Contains(w.Body.String(), "socket") || strings.Contains(w.Body.String(), "database") {
						t.Fatal("private owner diagnostics escaped")
					}
				}
			})
		}
		large := []byte(`{"binding":` + binding + `,"extra":"` + strings.Repeat("x", panelruntime.NodeAuthorityMessageLimit) + `"}`)
		malformed := []struct {
			name       string
			body       []byte
			compressed bool
			status     int
		}{
			{"null", []byte(`null`), false, 400},
			{"null-binding", []byte(strings.Replace(string(body), binding, `null`, 1)), false, 400},
			{"duplicate-binding", []byte(strings.Replace(string(body), `"binding":`, `"binding":`+binding+`,"binding":`, 1)), false, 400},
			{"duplicate-inner", []byte(strings.Replace(string(body), `"nodeId":"node-a"`, `"nodeId":"old","\u006eodeId":"node-a"`, 1)), false, 400},
			{"case-alias", []byte(strings.Replace(string(body), `"expectedBootId"`, `"ExpectedBootId"`, 1)), false, 400},
			{"null-generation", []byte(strings.Replace(string(body), `"generation":3`, `"generation":null`, 1)), false, 400},
			{"string-generation", []byte(strings.Replace(string(body), `"generation":3`, `"generation":"3"`, 1)), false, 400},
			{"trailing", append(append([]byte{}, body...), []byte(` {}`)...), false, 400},
			{"wire-limit", large, false, 413},
			{"decoded-limit", large, true, 400},
		}
		if op == "install" || op == "renew" {
			malformed = append(malformed,
				struct {
					name       string
					body       []byte
					compressed bool
					status     int
				}{"numeric-uint64", []byte(strings.Replace(string(body), `"sequence":"1"`, `"sequence":1`, 1)), false, 400},
				struct {
					name       string
					body       []byte
					compressed bool
					status     int
				}{"nested-duplicate", []byte(strings.Replace(string(body), `"clientId":"client-a"`, `"clientId":"old","clientId":"client-a"`, 1)), false, 400},
				struct {
					name       string
					body       []byte
					compressed bool
					status     int
				}{"protobuf-alias", []byte(strings.Replace(string(body), `"leaseDurationMillis"`, `"lease_duration_millis"`, 1)), false, 400},
			)
		}
		for _, tc := range malformed {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				w := request(op, "https", http.MethodPost, model.ApiScopeAdmin, tc.body, tc.compressed, false, nil, "")
				if w.Code != tc.status {
					t.Fatalf("status=%d want=%d", w.Code, tc.status)
				}
			})
		}
	}
	engine.GET("/control-test-login", func(c *gin.Context) {
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
	engine.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "https://node.example/control-test-login", nil))
	if login.Code != 200 {
		t.Fatalf("login status=%d", login.Code)
	}
	last := map[string]*http.Cookie{}
	for _, cookie := range login.Result().Cookies() {
		last[cookie.Name] = cookie
	}
	cookies := make([]*http.Cookie, 0, len(last))
	for _, cookie := range last {
		cookies = append(cookies, cookie)
	}
	for _, op := range []string{"requests", "install", "get", "pause", "seal", "renew"} {
		for _, withToken := range []bool{false, true} {
			t.Run(op+"/session-"+map[bool]string{false: "missing", true: "csrf"}[withToken], func(t *testing.T) {
				csrf := ""
				want := 403
				if withToken {
					csrf = login.Body.String()
					want = 200
				}
				w := request(op, "https", http.MethodPost, "", []byte(bodies[op]), false, false, cookies, csrf)
				if w.Code != want {
					t.Fatalf("session status=%d want=%d", w.Code, want)
				}
			})
		}
	}
	for _, tc := range []struct {
		name     string
		result   any
		accepted bool
	}{
		{"small-valid", json.RawMessage(`{"requests":[]}`), true},
		{"complete-envelope-exceeds-limit", json.RawMessage(`"` + strings.Repeat("x", panelruntime.NodeAuthorityMessageLimit-12) + `"`), false},
		{"encoding-failure", make(chan int), false},
	} {
		t.Run("response/"+tc.name, func(t *testing.T) {
			responseEngine := gin.New()
			responseEngine.GET("/", func(c *gin.Context) { writeNodeAuthorityControl(c, tc.result, nil) })
			w := httptest.NewRecorder()
			responseEngine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://node.example/", nil))
			var envelope struct {
				Success bool            `json:"success"`
				Obj     json.RawMessage `json:"obj"`
				Msg     string          `json:"msg"`
			}
			if w.Code != 200 || len(w.Body.Bytes()) > panelruntime.NodeAuthorityMessageLimit || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Success != tc.accepted {
				t.Fatalf("invalid bounded response: status=%d length=%d", w.Code, w.Body.Len())
			}
			if !tc.accepted && (string(envelope.Obj) != "null" || !strings.Contains(envelope.Msg, panelruntime.ErrNodeAuthorityDiscovery.Error())) {
				t.Fatal("encoding error did not return opaque failure")
			}
		})
	}

}
