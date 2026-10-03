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
	command "github.com/xtls/xray-core/app/clientpolicy/command"
)

func authorityRemoteFixture() *NodeAuthorityDiscovery {
	return &NodeAuthorityDiscovery{
		Capabilities: &command.Capabilities{ApiVersion: 1, InstanceId: "private-node-instance", BootId: strings.Repeat("a", 32), Capabilities: []string{"fresh-core-incarnation-v1", "monotonic-authority-challenge-v1", "boot-bound-execution-grants-v1"}},
		Challenge:    &command.AuthorityChallenge{InstanceId: "private-node-instance", BootId: strings.Repeat("a", 32), ChallengeId: strings.Repeat("b", 32), MaxDurationMillis: 10000},
	}
}

func authorityRemoteEnvelope(t *testing.T, object any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"success": true, "obj": object})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRemoteAuthorityDiscoveryRequiresVerifiedTLS(t *testing.T) {
	codec, err := nodetoken.NewCodec(nodetoken.ModeRequired, &nodetoken.Keyring{ActiveID: "synthetic-key", Keys: map[string][32]byte{"synthetic-key": {1, 2, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	nodetoken.Init(codec)
	t.Cleanup(func() { nodetoken.Init(nil) })
	stored, err := nodetoken.Encrypt(1, "authority-synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	response := authorityRemoteEnvelope(t, authorityRemoteFixture())
	var reached atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reached.Store(true)
		if req.Method != http.MethodPost || req.URL.Path != "/panel/api/server/clientPolicyAuthority" || req.Header.Get("Authorization") != "Bearer authority-synthetic-token" || req.Header.Get(wirecodec.HashHeader) == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write(response)
	}))
	defer srv.Close()
	node := nodeForServer(t, srv, "pin", leafPinBase64(srv))
	node.ApiToken = stored
	request := AuthorityDiscoveryRequest{ExpectedInstanceID: "private-node-instance", ExpectedBootID: strings.Repeat("a", 32)}
	if result, err := NewRemote(node, nil).DiscoverAuthority(context.Background(), request); err != nil || result == nil || result.Capabilities.BootId != request.ExpectedBootID || !reached.Load() {
		t.Fatalf("verified real TLS/encrypted-token discovery failed: %v", err)
	}
	t.Run("existing-default-https-scheme", func(t *testing.T) {
		copy := *node
		copy.Scheme = ""
		if result, err := NewRemote(&copy, nil).DiscoverAuthority(context.Background(), request); err != nil || result == nil {
			t.Fatal("existing default verified HTTPS scheme was rejected")
		}
	})
	for _, tc := range []struct {
		name   string
		mutate func(*model.Node)
	}{
		{"skip-verification", func(n *model.Node) { n.TlsVerifyMode = "skip" }},
		{"disabled-node", func(n *model.Node) { n.Enable = false }},
		{"transitive-node", func(n *model.Node) { n.Transitive = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *node
			tc.mutate(&copy)
			reached.Store(false)
			if result, err := NewRemote(&copy, nil).DiscoverAuthority(context.Background(), request); err == nil || result != nil || reached.Load() {
				t.Fatal("unsupported node authority transport reached a successful peer")
			}
		})
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		_, _ = w.Write(response)
	}))
	defer plain.Close()
	t.Run("plain-http", func(t *testing.T) {
		reached.Store(false)
		plainNode := nodeForPlainServer(t, plain, "verify", stored)
		if result, err := NewRemote(plainNode, nil).DiscoverAuthority(context.Background(), request); err == nil || result != nil || reached.Load() {
			t.Fatal("node authority discovery accepted plaintext HTTP")
		}
	})
	t.Run("https-redirect-to-http", func(t *testing.T) {
		reached.Store(false)
		redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { http.Redirect(w, req, plain.URL, http.StatusFound) }))
		defer redirect.Close()
		node := nodeForServer(t, redirect, "pin", leafPinBase64(redirect))
		node.ApiToken = stored
		if result, err := NewRemote(node, nil).DiscoverAuthority(context.Background(), request); err == nil || result != nil || reached.Load() {
			t.Fatal("node authority discovery followed an unbound HTTP redirect")
		}
	})
}

func TestRemoteAuthorityDiscoveryRejectsInvalidResponse(t *testing.T) {
	valid := string(authorityRemoteEnvelope(t, authorityRemoteFixture()))
	delegated := authorityRemoteFixture()
	delegated.ExecutionRole = &NodeExecutionRole{Mode: NodeExecutionDelegated, AuthorityID: "coordinator", Generation: 3, NodeID: "node-a"}
	withRole := string(authorityRemoteEnvelope(t, delegated))
	rawRole, err := json.Marshal(delegated.ExecutionRole)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*NodeAuthorityDiscovery)
		raw    string
		bound  bool
	}{
		{name: "null-capabilities", change: func(d *NodeAuthorityDiscovery) { d.Capabilities = nil }},
		{name: "null-challenge", change: func(d *NodeAuthorityDiscovery) { d.Challenge = nil }},
		{name: "wrong-api", change: func(d *NodeAuthorityDiscovery) { d.Capabilities.ApiVersion = 2 }},
		{name: "wrong-instance", change: func(d *NodeAuthorityDiscovery) { d.Capabilities.InstanceId = "different-instance" }},
		{name: "explicit-null-execution-role", raw: strings.Replace(valid, `"challenge":`, `"executionRole":null,"challenge":`, 1)},
		{name: "delegated-role-erased-by-duplicate-null", raw: strings.Replace(withRole, `"executionRole":`+string(rawRole), `"executionRole":`+string(rawRole)+`,"executionRole":null`, 1)},
		{name: "case-alias-execution-role", raw: strings.Replace(withRole, `"executionRole":`, `"ExecutionRole":`, 1)},
		{name: "duplicate-role-binding", raw: strings.Replace(withRole, `"authorityId":`, `"authorityId":"other","authorityId":`, 1)},
		{name: "case-alias-role-binding", raw: strings.Replace(withRole, `"authorityId":`, `"AuthorityId":`, 1)},
		{name: "duplicate-role-mode", raw: strings.Replace(withRole, `"mode":`, `"mode":"local","mode":`, 1)},
		{name: "unknown-execution-role", change: func(d *NodeAuthorityDiscovery) {
			d.ExecutionRole = &NodeExecutionRole{Mode: "unknown"}
		}},
		{name: "delegated-role-without-binding", change: func(d *NodeAuthorityDiscovery) {
			d.ExecutionRole = &NodeExecutionRole{Mode: NodeExecutionDelegated}
		}},
		{name: "local-role-with-delegated-binding", change: func(d *NodeAuthorityDiscovery) {
			d.ExecutionRole = &NodeExecutionRole{Mode: NodeExecutionLocal, AuthorityID: "coordinator", Generation: 1, NodeID: "node"}
		}},
		{name: "wrong-boot", change: func(d *NodeAuthorityDiscovery) { d.Challenge.BootId = strings.Repeat("c", 32) }},
		{name: "invalid-nonce", change: func(d *NodeAuthorityDiscovery) { d.Challenge.ChallengeId = "invalid" }},
		{name: "missing-feature", change: func(d *NodeAuthorityDiscovery) { d.Capabilities.Capabilities = []string{"fresh-core-incarnation-v1"} }},
		{name: "excess-duration", change: func(d *NodeAuthorityDiscovery) { d.Challenge.MaxDurationMillis = 10001 }},
		{name: "malformed-json", raw: "{invalid"},
		{name: "unknown-envelope-field", raw: strings.Replace(valid, `"success":true`, `"unrecognized":1,"success":true`, 1)},
		{name: "unknown-object-field", raw: strings.Replace(valid, `"challenge":`, `"unrecognized":1,"challenge":`, 1)},
		{name: "oversized-valid-response", change: func(d *NodeAuthorityDiscovery) {
			d.Capabilities.CoreVersion = strings.Repeat("x", NodeAuthorityMessageLimit)
		}},
		{name: "stale-bound-boot", bound: true, change: func(d *NodeAuthorityDiscovery) {
			d.Capabilities.BootId = strings.Repeat("c", 32)
			d.Challenge.BootId = d.Capabilities.BootId
		}},
		{name: "trailing-response", raw: valid + valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := authorityRemoteFixture()
			if tc.change != nil {
				tc.change(object)
			}
			raw := authorityRemoteEnvelope(t, object)
			if tc.raw != "" {
				raw = []byte(tc.raw)
			}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
			defer srv.Close()
			remote := NewRemote(nodeForServer(t, srv, "pin", leafPinBase64(srv)), nil)
			request := AuthorityDiscoveryRequest{}
			if tc.bound {
				request = AuthorityDiscoveryRequest{ExpectedInstanceID: "private-node-instance", ExpectedBootID: strings.Repeat("a", 32)}
			}
			if result, err := remote.DiscoverAuthority(context.Background(), request); err == nil || result != nil {
				t.Fatal("invalid node authority response produced a successful discovery")
			}
		})
	}
	t.Run("partial-binding-and-cancellation", func(t *testing.T) {
		var reached atomic.Bool
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached.Store(true); _, _ = w.Write([]byte(valid)) }))
		defer srv.Close()
		remote := NewRemote(nodeForServer(t, srv, "pin", leafPinBase64(srv)), nil)
		if result, err := remote.DiscoverAuthority(context.Background(), AuthorityDiscoveryRequest{ExpectedInstanceID: "private-node-instance"}); err == nil || result != nil || reached.Load() {
			t.Fatal("partial binding was sent as initial node discovery")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reached.Store(false)
		if result, err := remote.DiscoverAuthority(ctx, AuthorityDiscoveryRequest{}); err == nil || result != nil || reached.Load() {
			t.Fatal("canceled discovery reached a successful peer")
		}
	})
}
