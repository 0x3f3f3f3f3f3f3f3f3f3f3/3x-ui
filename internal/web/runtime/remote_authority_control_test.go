package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mhsanaei/3x-ui/v3/internal/crypto/nodetoken"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/netsafe"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Independent literal expected wire data: no production serializer computes
// expected field names or the integer above JavaScript's exact-number range.
func controlRemoteFixture() (NodeAuthorityControlBinding, *command.ExecutionGrant) {
	b := NodeAuthorityControlBinding{ExpectedInstanceID: "private-node-instance", ExpectedBootID: strings.Repeat("a", 32), AuthorityID: "coordinator", Generation: 3, NodeID: "node-a"}
	g := &command.ExecutionGrant{Authority: &command.AuthorityBinding{AuthorityId: "coordinator", Generation: 3, NodeId: "node-a"}, InstanceId: b.ExpectedInstanceID, BootId: b.ExpectedBootID, ClientId: "client-a", WindowId: "window-a", PolicyVersion: 1, GrantId: "grant-a", Sequence: 1, ChallengeId: strings.Repeat("b", 32), Capacity: 9007199254740993, Upload: &command.AuthorityShare{Unlimited: true}, Download: &command.AuthorityShare{Unlimited: true}, LeaseDurationMillis: 1000}
	return b, g
}

func controlRemoteIdentityJSON() string {
	return fmt.Sprintf(`"instanceId":"private-node-instance","bootId":%q,"executionRole":{"mode":"delegated","authorityId":"coordinator","generation":3,"nodeId":"node-a"}`, strings.Repeat("a", 32))
}

func controlRemoteGrantJSON() string {
	return fmt.Sprintf(`{"authority":{"authorityId":"coordinator","generation":"3","nodeId":"node-a"},"instanceId":"private-node-instance","bootId":%q,"clientId":"client-a","windowId":"window-a","policyVersion":"1","grantId":"grant-a","sequence":"1","challengeId":%q,"capacity":"9007199254740993","upload":{"unlimited":true,"rate":"0","burst":"0"},"download":{"unlimited":true,"rate":"0","burst":"0"},"leaseDurationMillis":"1000"}`, strings.Repeat("a", 32), strings.Repeat("b", 32))
}

func controlRemoteResponseJSON(operation string) string {
	var payload string
	switch operation {
	case "requests":
		payload = fmt.Sprintf(`"requests":{"instanceId":"private-node-instance","bootId":%q,"requests":[{"requestId":%q,"clientId":"client-a","policyVersion":"1","previousGrantId":""}]}`, strings.Repeat("a", 32), strings.Repeat("c", 32))
	case "renew":
		payload = fmt.Sprintf(`"renewal":{"expectedBootId":%q,"clientId":"client-a","grantId":"grant-a","challengeId":%q,"sequence":"1","leaseDurationMillis":"1000"}`, strings.Repeat("a", 32), strings.Repeat("b", 32))
	default:
		payload = fmt.Sprintf(`"state":{"grant":%s,"usage":{"rawUpload":"0","rawDownload":"0","billedBytes":"0","remainder":"0"},"sequence":"1","sealed":%t}`, controlRemoteGrantJSON(), operation == "seal" || operation == "pause")
	}
	return `{"success":true,"msg":"","obj":{` + controlRemoteIdentityJSON() + `,` + payload + `}}`
}

func assertControlFixtureJSON(t *testing.T, body string) {
	t.Helper()
	if !json.Valid([]byte(body)) {
		t.Fatal("invalid test JSON cannot prove semantic rejection")
	}
}

func TestRemoteAuthorityControlRequiresVerifiedTLS(t *testing.T) {
	codec, err := nodetoken.NewCodec(nodetoken.ModeRequired, &nodetoken.Keyring{ActiveID: "synthetic-key", Keys: map[string][32]byte{"synthetic-key": {1, 2, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	nodetoken.Init(codec)
	t.Cleanup(func() { nodetoken.Init(nil) })
	stored, err := nodetoken.Encrypt(1, "control-synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	binding, grant := controlRemoteFixture()
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, err := io.ReadAll(io.LimitReader(r.Body, NodeAuthorityMessageLimit+1))
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var fields map[string]json.RawMessage
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer control-synthetic-token" || r.Header.Get(wirecodec.HashHeader) == "" || json.Unmarshal(raw, &fields) != nil || fields["binding"] == nil {
			t.Error("unbound or unauthenticated control request")
			w.WriteHeader(400)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/install") && (!strings.Contains(string(raw), `"capacity":"9007199254740993"`) || strings.Contains(string(raw), `"lease_duration_millis"`)) {
			t.Error("grant integer or canonical name lost on wire")
			w.WriteHeader(400)
			return
		}
		operation := strings.TrimPrefix(r.URL.Path, "/panel/api/server/clientPolicyAuthority/")
		if operation == r.URL.Path {
			t.Error("wrong control endpoint")
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(controlRemoteResponseJSON(operation)))
	}))
	defer srv.Close()
	node := nodeForServer(t, srv, "pin", leafPinBase64(srv))
	node.ApiToken = stored
	remote := NewRemote(node, nil)
	page, err := remote.ReadAuthorityRequests(context.Background(), NodeAuthorityRequestsRequest{Binding: binding, Limit: 128})
	if err != nil || page == nil || len(page.Requests.Requests) != 1 || page.Requests.Requests[0].ClientId != "client-a" {
		t.Fatalf("real TLS control demand unavailable: %+v/%v", page, err)
	}
	installed, err := remote.InstallAuthorityGrant(context.Background(), NodeAuthorityInstallRequest{Binding: binding, Grant: grant})
	if err != nil || installed == nil || installed.State.Grant.Capacity != 9007199254740993 {
		t.Fatalf("large exact grant lost: %+v/%v", installed, err)
	}
	request := NodeAuthorityGrantRequest{Binding: binding, ClientID: "client-a", GrantID: "grant-a"}
	for _, operation := range []string{"get", "pause", "seal"} {
		t.Run(operation, func(t *testing.T) {
			var result *NodeAuthorityGrantResult
			var err error
			switch operation {
			case "get":
				result, err = remote.GetAuthorityGrant(context.Background(), request)
			case "pause":
				result, err = remote.PauseAuthorityGrant(context.Background(), request)
			case "seal":
				result, err = remote.SealAuthorityGrant(context.Background(), request)
			}
			if err != nil || result == nil || result.State.Sealed != (operation != "get") {
				t.Fatalf("real TLS operation unavailable: %+v/%v", result, err)
			}
		})
	}
	renewal := &command.AuthorityRenewalRequest{ExpectedBootId: binding.ExpectedBootID, ClientId: "client-a", GrantId: "grant-a", ChallengeId: strings.Repeat("b", 32), Sequence: 1, LeaseDurationMillis: 1000}
	ack, err := remote.RenewAuthorityGrant(context.Background(), NodeAuthorityRenewalRequest{Binding: binding, Renewal: renewal})
	if err != nil || ack == nil || ack.Renewal.Sequence != 1 || ack.Renewal.ExpectedBootId != binding.ExpectedBootID {
		t.Fatalf("real TLS renewal unavailable: %+v/%v", ack, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*model.Node)
	}{
		{"disabled", func(n *model.Node) { n.Enable = false }}, {"transitive", func(n *model.Node) { n.Transitive = true }}, {"skip-verification", func(n *model.Node) { n.TlsVerifyMode = "skip" }}, {"private-without-opt-in", func(n *model.Node) { n.AllowPrivateAddress = false }}, {"untrusted-cert", func(n *model.Node) { n.TlsVerifyMode = "verify" }}, {"wrong-pin", func(n *model.Node) { n.PinnedCertSha256 = strings.Repeat("A", 43) + "=" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *node
			tc.change(&copy)
			before := calls.Load()
			if result, err := NewRemote(&copy, nil).InstallAuthorityGrant(context.Background(), NodeAuthorityInstallRequest{Binding: binding, Grant: grant}); err == nil || result != nil || calls.Load() != before {
				t.Fatal("unsafe transport reached control peer")
			}
		})
	}
	for _, ctx := range []context.Context{nil, func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }()} {
		before := calls.Load()
		if result, err := remote.InstallAuthorityGrant(ctx, NodeAuthorityInstallRequest{Binding: binding, Grant: grant}); err == nil || result != nil || calls.Load() != before {
			t.Fatal("invalid context reached wire")
		}
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(controlRemoteResponseJSON("install")))
	}))
	defer plain.Close()
	before := calls.Load()
	if result, err := NewRemote(nodeForPlainServer(t, plain, "verify", stored), nil).InstallAuthorityGrant(context.Background(), NodeAuthorityInstallRequest{Binding: binding, Grant: grant}); err == nil || result != nil || calls.Load() != before {
		t.Fatal("plaintext control accepted")
	}
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, plain.URL, 302) }))
	defer redirect.Close()
	redirectNode := nodeForServer(t, redirect, "pin", leafPinBase64(redirect))
	redirectNode.ApiToken = stored
	if result, err := NewRemote(redirectNode, nil).InstallAuthorityGrant(context.Background(), NodeAuthorityInstallRequest{Binding: binding, Grant: grant}); err == nil || result != nil || calls.Load() != before {
		t.Fatal("control followed redirect")
	}
}

func TestRemoteAuthorityControlRejectsAmbiguousPayloads(t *testing.T) {
	binding, grant := controlRemoteFixture()
	valid := controlRemoteResponseJSON("install")
	assertControlFixtureJSON(t, valid)
	positive := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(valid)) }))
	defer positive.Close()
	if result, err := NewRemote(nodeForServer(t, positive, "pin", leafPinBase64(positive)), nil).InstallAuthorityGrant(context.Background(), NodeAuthorityInstallRequest{Binding: binding, Grant: grant}); err != nil || result == nil {
		t.Fatalf("complete canonical control response unavailable: %v", err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"duplicate-envelope", `"success":true`, `"success":false,"success":true`},
		{"alias-envelope", `"success":true`, `"Success":true`},
		{"duplicate-role", `"executionRole":`, `"executionRole":null,"executionRole":`},
		{"alias-role", `"executionRole":`, `"ExecutionRole":`},
		{"wrong-source", `"instanceId":"private-node-instance"`, `"instanceId":"wrong-source"`},
		{"wrong-boot", `"bootId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"bootId":"dddddddddddddddddddddddddddddddd"`},
		{"wrong-role-authority", `"authorityId":"coordinator","generation":3`, `"authorityId":"other","generation":3`},
		{"wrong-role-generation", `"generation":3`, `"generation":4`},
		{"wrong-role-node", `"nodeId":"node-a"`, `"nodeId":"other-node"`},
		{"unknown-object-field", `"state":`, `"unknown":true,"state":`},
		{"null-role", `"executionRole":{"mode":"delegated","authorityId":"coordinator","generation":3,"nodeId":"node-a"}`, `"executionRole":null`},
		{"zero-state-sequence", `"sequence":"1","sealed":false`, `"sequence":"0","sealed":false`},
		{"numeric-fraction", `"remainder":"0"`, `"remainder":0`},
		{"wrong-client", `"clientId":"client-a"`, `"clientId":"different-client"`},
		{"wrong-grant", `"grantId":"grant-a"`, `"grantId":"different-grant"`},
		{"duplicate-inner-authority", `"authorityId":"coordinator","generation":"3"`, `"authorityId":"other","authorityId":"coordinator","generation":"3"`},
		{"alias-inner-authority", `"authorityId":"coordinator","generation":"3"`, `"AuthorityId":"coordinator","generation":"3"`},
		{"numeric-uint64", `"capacity":"9007199254740993"`, `"capacity":9007199254740993`},
		{"leading-zero", `"capacity":"9007199254740993"`, `"capacity":"09007199254740993"`},
		{"signed-integer", `"capacity":"9007199254740993"`, `"capacity":"+9007199254740993"`},
		{"exponent", `"capacity":"9007199254740993"`, `"capacity":"9e15"`},
		{"overflow", `"capacity":"9007199254740993"`, `"capacity":"18446744073709551616"`},
		{"null-share", `"upload":{"unlimited":true,"rate":"0","burst":"0"}`, `"upload":null`},
		{"duplicate-share", `"unlimited":true,"rate":"0"`, `"unlimited":true,"rate":"1","rate":"0"`},
		{"alias-usage", `"rawUpload":"0"`, `"raw_upload":"0"`},
		{"duplicate-usage", `"rawUpload":"0"`, `"rawUpload":"1","rawUpload":"0"`},
		{"missing-usage-field", `"rawUpload":"0",`, ``},
		{"overcapacity-billing", `"billedBytes":"0"`, `"billedBytes":"9007199254740994"`},
		{"invalid-fraction", `"remainder":"0"`, `"remainder":"1000000"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(valid, tc.from, tc.to, 1)
			if raw == valid {
				t.Fatal("fixture did not change")
			}
			assertControlFixtureJSON(t, raw)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(raw)) }))
			defer srv.Close()
			if result, err := NewRemote(nodeForServer(t, srv, "pin", leafPinBase64(srv)), nil).InstallAuthorityGrant(context.Background(), NodeAuthorityInstallRequest{Binding: binding, Grant: grant}); err == nil || result != nil {
				t.Fatal("ambiguous control response accepted")
			}
		})
	}
	for _, raw := range []string{"null", valid + valid, strings.Repeat(" ", NodeAuthorityMessageLimit) + valid} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(raw)) }))
		if result, err := NewRemote(nodeForServer(t, srv, "pin", leafPinBase64(srv)), nil).InstallAuthorityGrant(context.Background(), NodeAuthorityInstallRequest{Binding: binding, Grant: grant}); err == nil || result != nil {
			t.Error("unbounded/trailing/null response accepted")
		}
		srv.Close()
	}
}

func TestNodeHTTPClientDoesNotReusePrivateOptInConnections(t *testing.T) {
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	allowed := nodeForPlainServer(t, srv, "verify", "synthetic-token")
	allowed.AllowPrivateAddress = true
	client, err := HTTPClientForNode(allowed, "")
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(netsafe.ContextWithAllowPrivate(context.Background(), true), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	refused := *allowed
	refused.AllowPrivateAddress = false
	client, err = HTTPClientForNode(&refused, "")
	if err != nil {
		t.Fatal(err)
	}
	request, err = http.NewRequestWithContext(netsafe.ContextWithAllowPrivate(context.Background(), false), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || reached.Load() != 1 {
		t.Fatal("private opt-out reused an opt-in connection")
	}
}

func TestNodeAuthorityControlWirePreservesEmptyDemandAndExactAmounts(t *testing.T) {
	binding, grant := controlRemoteFixture()
	request := NodeAuthorityInstallRequest{Binding: binding, Grant: grant}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"capacity":"9007199254740993"`) || strings.Contains(string(raw), `"lease_duration_millis"`) {
		t.Fatal("canonical exact grant lost")
	}
	var decoded NodeAuthorityInstallRequest
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Grant.Capacity != 9007199254740993 || decoded.Binding != binding {
		t.Fatalf("exact request roundtrip failed: %v", err)
	}
	empty := NodeAuthorityRequestsResult{NodeAuthorityControlIdentity: NodeAuthorityControlIdentity{InstanceID: binding.ExpectedInstanceID, BootID: binding.ExpectedBootID, ExecutionRole: binding.Role()}, Requests: &command.AuthorityRequests{InstanceId: binding.ExpectedInstanceID, BootId: binding.ExpectedBootID}}
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"requests":[]`) || strings.Contains(string(raw), `null`) {
		t.Fatal("empty demand was not an explicit array")
	}
	var page NodeAuthorityRequestsResult
	if err := json.Unmarshal(raw, &page); err != nil || page.Requests == nil || len(page.Requests.Requests) != 0 {
		t.Fatalf("empty canonical demand rejected: %v", err)
	}
	for _, tc := range []struct{ from, to string }{
		{`"generation":3`, `"generation":null`},
		{`"binding":`, `"Binding":`},
		{`"generation":"3"`, `"Generation":"3"`},
		{`"capacity":"9007199254740993"`, `"capacity":9007199254740993`},
		{`"unlimited":true`, `"unlimited":null`},
	} {
		original, _ := json.Marshal(request)
		bad := strings.Replace(string(original), tc.from, tc.to, 1)
		if bad == string(original) {
			t.Fatal("request fixture unchanged")
		}
		assertControlFixtureJSON(t, bad)
		var got NodeAuthorityInstallRequest
		if json.Unmarshal([]byte(bad), &got) == nil {
			t.Fatal("ambiguous incoming control request accepted")
		}
	}
}
