package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/crypto/nodetoken"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
)

func delegationRemoteFixture() (NodeDelegationRequest, *NodeDelegationResult) {
	request := NodeDelegationRequest{AuthorityID: "coordinator", Generation: 3, NodeID: "node-a"}
	return request, &NodeDelegationResult{InstanceID: "private-node-instance", Role: request.Role()}
}

func TestRemoteNodeDelegationRequiresVerifiedTLS(t *testing.T) {
	codec, err := nodetoken.NewCodec(nodetoken.ModeRequired, &nodetoken.Keyring{ActiveID: "synthetic-key", Keys: map[string][32]byte{"synthetic-key": {1, 2, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	nodetoken.Init(codec)
	t.Cleanup(func() { nodetoken.Init(nil) })
	stored, err := nodetoken.Encrypt(1, "delegation-synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	request, result := delegationRemoteFixture()
	response := authorityRemoteEnvelope(t, result)
	var reached atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reached.Store(true)
		var got NodeDelegationRequest
		if req.Method != http.MethodPost || req.URL.Path != "/panel/api/server/clientPolicyDelegation" || req.Header.Get("Authorization") != "Bearer delegation-synthetic-token" || req.Header.Get(wirecodec.HashHeader) == "" || json.NewDecoder(req.Body).Decode(&got) != nil || got != request {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write(response)
	}))
	defer srv.Close()
	node := nodeForServer(t, srv, "pin", leafPinBase64(srv))
	node.ApiToken = stored
	if got, err := NewRemote(node, nil).ConfigureDelegation(context.Background(), request); err != nil || got == nil || *got != *result || !reached.Load() {
		t.Fatalf("verified real TLS/encrypted-token delegation failed: %+v/%v", got, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*model.Node)
	}{
		{"default-https", func(n *model.Node) { n.Scheme = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *node
			tc.change(&copy)
			if got, err := NewRemote(&copy, nil).ConfigureDelegation(context.Background(), request); err != nil || got == nil {
				t.Fatalf("default verified HTTPS failed: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*model.Node)
	}{
		{"skip-verification", func(n *model.Node) { n.TlsVerifyMode = "skip" }},
		{"disabled", func(n *model.Node) { n.Enable = false }},
		{"transitive", func(n *model.Node) { n.Transitive = true }},
		{"private-without-opt-in", func(n *model.Node) { n.AllowPrivateAddress = false }},
		{"untrusted-certificate", func(n *model.Node) { n.TlsVerifyMode = "verify" }},
		{"wrong-pin", func(n *model.Node) { n.PinnedCertSha256 = strings.Repeat("A", 43) + "=" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *node
			tc.change(&copy)
			reached.Store(false)
			if got, err := NewRemote(&copy, nil).ConfigureDelegation(context.Background(), request); err == nil || got != nil || reached.Load() {
				t.Fatal("unsupported TLS node reached successful delegation peer")
			}
		})
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached.Store(true); _, _ = w.Write(response) }))
	defer plain.Close()
	t.Run("plaintext", func(t *testing.T) {
		reached.Store(false)
		if got, err := NewRemote(nodeForPlainServer(t, plain, "verify", stored), nil).ConfigureDelegation(context.Background(), request); err == nil || got != nil || reached.Load() {
			t.Fatal("delegation used plaintext HTTP")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		reached.Store(false)
		redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { http.Redirect(w, req, plain.URL, http.StatusFound) }))
		defer redirect.Close()
		copy := nodeForServer(t, redirect, "pin", leafPinBase64(redirect))
		copy.ApiToken = stored
		if got, err := NewRemote(copy, nil).ConfigureDelegation(context.Background(), request); err == nil || got != nil || reached.Load() {
			t.Fatal("delegation followed a redirect")
		}
	})
	t.Run("invalid-request-before-wire", func(t *testing.T) {
		reached.Store(false)
		if got, err := NewRemote(node, nil).ConfigureDelegation(context.Background(), NodeDelegationRequest{}); err == nil || got != nil || reached.Load() {
			t.Fatal("invalid setup reached peer")
		}
	})
	t.Run("canceled-before-wire", func(t *testing.T) {
		reached.Store(false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got, err := NewRemote(node, nil).ConfigureDelegation(ctx, request); err == nil || got != nil || reached.Load() {
			t.Fatal("canceled setup reached peer")
		}
	})
	t.Run("nil-context", func(t *testing.T) {
		if got, err := NewRemote(node, nil).ConfigureDelegation(nil, request); err == nil || got != nil {
			t.Fatal("nil context accepted")
		}
	})
}

func TestRemoteNodeDelegationRejectsInvalidResponse(t *testing.T) {
	request, result := delegationRemoteFixture()
	valid := string(authorityRemoteEnvelope(t, result))
	for _, tc := range []struct {
		name, raw string
		change    func(*NodeDelegationResult)
	}{
		{name: "missing-instance", change: func(r *NodeDelegationResult) { r.InstanceID = "" }},
		{name: "wrong-authority", change: func(r *NodeDelegationResult) { r.Role.AuthorityID = "other" }},
		{name: "wrong-generation", change: func(r *NodeDelegationResult) { r.Role.Generation++ }},
		{name: "wrong-node", change: func(r *NodeDelegationResult) { r.Role.NodeID = "other" }},
		{name: "local-role", change: func(r *NodeDelegationResult) { r.Role = NodeExecutionRole{Mode: NodeExecutionLocal} }},
		{name: "oversize", change: func(r *NodeDelegationResult) { r.InstanceID = strings.Repeat("x", NodeAuthorityMessageLimit) }},
		{name: "root-null", raw: `null`},
		{name: "null-object", raw: `{"success":true,"obj":null}`},
		{name: "null-role", raw: `{"success":true,"obj":{"instanceId":"private-node-instance","role":null}}`},
		{name: "unknown-field", raw: strings.Replace(valid, `"instanceId":`, `"extra":1,"instanceId":`, 1)},
		{name: "alias-instance", raw: strings.Replace(valid, `"instanceId":`, `"InstanceId":`, 1)},
		{name: "duplicate-instance", raw: strings.Replace(valid, `"instanceId":`, `"instanceId":"old","instanceId":`, 1)},
		{name: "duplicate-role-binding", raw: strings.Replace(valid, `"authorityId":`, `"authorityId":"other","authorityId":`, 1)},
		{name: "alias-role-binding", raw: strings.Replace(valid, `"authorityId":`, `"AuthorityId":`, 1)},
		{name: "duplicate-envelope", raw: strings.Replace(valid, `"success":true`, `"success":false,"success":true`, 1)},
		{name: "alias-envelope", raw: strings.Replace(valid, `"success":true`, `"Success":true`, 1)},
		{name: "unknown-envelope", raw: strings.Replace(valid, `"success":true`, `"extra":1,"success":true`, 1)},
		{name: "trailing", raw: valid + valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *result
			if tc.change != nil {
				tc.change(&copy)
			}
			raw := authorityRemoteEnvelope(t, &copy)
			if tc.raw != "" {
				raw = []byte(tc.raw)
			}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
			defer srv.Close()
			if got, err := NewRemote(nodeForServer(t, srv, "pin", leafPinBase64(srv)), nil).ConfigureDelegation(context.Background(), request); err == nil || got != nil {
				t.Fatalf("invalid setup response accepted: %+v/%v", got, err)
			}
		})
	}
}
