package controller

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestNodeAuthorityDiscoveryHTTPAuthenticationAndBounds(t *testing.T) {
	engine, _ := newAPIAuthTestEngine(t)
	NewNodeAuthorityAPIController(engine.Group(""))
	for _, scope := range []string{model.ApiScopeAdmin, model.ApiScopeNodeSync, model.ApiScopeMonitor} {
		row := &model.ApiToken{Name: "authority-" + scope, Token: crypto.HashTokenSHA256("authority-" + scope), Enabled: true, Scope: scope}
		if err := database.GetDB().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	large := []byte(`{"expectedInstanceId":"` + strings.Repeat("x", panelruntime.NodeAuthorityMessageLimit) + `"}`)
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
		{name: "partial-binding", scope: model.ApiScopeAdmin, body: []byte(`{"expectedInstanceId":"source"}`), status: 400},
		{name: "unknown-field", scope: model.ApiScopeAdmin, body: []byte(`{"unknown":true}`), status: 400},
		{name: "trailing-json", scope: model.ApiScopeAdmin, body: []byte(`{} {}`), status: 400},
		{name: "stale-binding-erased-by-duplicates", scope: model.ApiScopeAdmin, body: []byte(`{"expectedInstanceId":"previous-instance","expectedBootId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expectedInstanceId":"","expectedBootId":""}`), status: 400},
		{name: "duplicate-same-value", scope: model.ApiScopeAdmin, body: []byte(`{"expectedInstanceId":"","expectedInstanceId":""}`), status: 400},
		{name: "stale-binding-erased-by-case-aliases", scope: model.ApiScopeAdmin, body: []byte(`{"expectedInstanceId":"previous-instance","expectedBootId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ExpectedInstanceId":"","ExpectedBootId":""}`), status: 400},
		{name: "case-alias-without-duplicate", scope: model.ApiScopeAdmin, body: []byte(`{"ExpectedInstanceId":"","ExpectedBootId":""}`), status: 400},
		{name: "root-null", scope: model.ApiScopeAdmin, body: []byte(`null`), status: 400},
		{name: "null-field-values", scope: model.ApiScopeAdmin, body: []byte(`{"expectedInstanceId":null,"expectedBootId":null}`), status: 400},
		{name: "compressed-duplicate-fields", scope: model.ApiScopeAdmin, body: []byte(`{"expectedBootId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expectedBootId":""}`), compressed: true, status: 400},
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
				body = []byte(`{}`)
			}
			wire := body
			if tc.compressed {
				wire = wirecodec.Compress(body)
			}
			req := httptest.NewRequest(method, scheme+"://node.example/panel/api/server/clientPolicyAuthority", bytes.NewReader(wire))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			if tc.scope != "" {
				req.Header.Set("Authorization", "Bearer authority-"+tc.scope)
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
			if tc.status == 200 && (!strings.Contains(w.Body.String(), `"success":false`) || !strings.Contains(w.Body.String(), "invalid or unavailable node authority discovery")) {
				t.Fatal("accepted route did not reach opaque owned-core validation")
			}
		})
	}
}
