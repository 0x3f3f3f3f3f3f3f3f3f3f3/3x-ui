package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/crypto/nodetoken"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
)

func clientMappingRemoteRequest() NodeClientMappingRequest {
	binding, _ := controlRemoteFixture()
	return NodeClientMappingRequest{Binding: binding, GlobalClientID: "11111111-1111-4111-8111-111111111111", LocalClientID: "22222222-2222-4222-8222-222222222222", GlobalPolicyVersion: 7, LocalPolicyVersion: 1, ExpectedPolicyDigest: strings.Repeat("a", 64)}
}

func clientMappingRemoteResponseJSON() string {
	return `{"success":true,"msg":"","obj":{` + controlRemoteIdentityJSON() + `,"mapping":{"authority":{"authorityId":"coordinator","generation":"3"},"nodeAnchor":{"authorityId":"node-original","generation":"1"},"nodeId":"node-a","sourceId":"private-node-instance","globalClientId":"11111111-1111-4111-8111-111111111111","localClientId":"22222222-2222-4222-8222-222222222222","globalPolicyVersion":"7","localPolicyVersion":"1","policyDigest":"` + strings.Repeat("a", 64) + `"}}}`
}

func TestRemoteClientMappingRequiresVerifiedProof(t *testing.T) {
	codec, err := nodetoken.NewCodec(nodetoken.ModeRequired, &nodetoken.Keyring{ActiveID: "synthetic-key", Keys: map[string][32]byte{"synthetic-key": {1, 2, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	nodetoken.Init(codec)
	t.Cleanup(func() { nodetoken.Init(nil) })
	stored, err := nodetoken.Encrypt(1, "mapping-synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	request := clientMappingRemoteRequest()
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, err := io.ReadAll(io.LimitReader(r.Body, NodeAuthorityMessageLimit+1))
		var received NodeClientMappingRequest
		if err != nil || r.Method != http.MethodPost || r.URL.Path != "/panel/api/server/clientPolicyAuthority/enroll" || r.Header.Get("Authorization") != "Bearer mapping-synthetic-token" || r.Header.Get(wirecodec.HashHeader) == "" || json.Unmarshal(raw, &received) != nil || received != request {
			t.Error("unbound, altered or unauthenticated enrollment wire")
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(clientMappingRemoteResponseJSON()))
	}))
	defer srv.Close()
	node := nodeForServer(t, srv, "pin", leafPinBase64(srv))
	node.ApiToken = stored
	remote := NewRemote(node, nil)
	proof, err := remote.EnrollClientMapping(context.Background(), request)
	if err != nil || proof.Validate(request) != nil || proof.Mapping.NodeAnchor.AuthorityID != "node-original" || calls.Load() != 1 {
		t.Fatalf("valid pinned TLS enrollment unavailable: %+v/%v", proof, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*model.Node)
	}{
		{"disabled", func(n *model.Node) { n.Enable = false }},
		{"transitive", func(n *model.Node) { n.Transitive = true }},
		{"plaintext", func(n *model.Node) { n.Scheme = "http" }},
		{"skip-verification", func(n *model.Node) { n.TlsVerifyMode = "skip" }},
		{"wrong-pin", func(n *model.Node) { n.PinnedCertSha256 = strings.Repeat("A", 43) + "=" }},
		{"untrusted-cert", func(n *model.Node) { n.TlsVerifyMode = "verify" }},
		{"private-no-opt-in", func(n *model.Node) { n.AllowPrivateAddress = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *node
			tc.change(&candidate)
			before := calls.Load()
			result, err := NewRemote(&candidate, nil).EnrollClientMapping(context.Background(), request)
			if err == nil || result != nil || calls.Load() != before {
				t.Fatal("unverified enrollment reached peer or succeeded")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		before := calls.Load()
		if result, err := remote.EnrollClientMapping(ctx, request); err == nil || result != nil || calls.Load() != before {
			t.Fatal("invalid context reached peer")
		}
	}
	for _, change := range []struct{ name, from, to string }{
		{"duplicate-envelope", `"success":true`, `"success":false,"success":true`},
		{"wrong-source", `"sourceId":"private-node-instance"`, `"sourceId":"wrong-source"`},
		{"wrong-boot", `"bootId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"bootId":"dddddddddddddddddddddddddddddddd"`},
		{"wrong-role", `"generation":3`, `"generation":4`},
		{"wrong-client", `"globalClientId":"11111111-1111-4111-8111-111111111111"`, `"globalClientId":"33333333-3333-4333-8333-333333333333"`},
		{"wrong-version", `"globalPolicyVersion":"7"`, `"globalPolicyVersion":"8"`},
		{"wrong-digest", strings.Repeat("a", 64), strings.Repeat("b", 64)},
		{"numeric-version", `"localPolicyVersion":"1"`, `"localPolicyVersion":1`},
		{"alias", `"nodeAnchor"`, `"NodeAnchor"`},
		{"unknown", `"mapping":{`, `"mapping":{"untrusted":true,`},
	} {
		t.Run(change.name, func(t *testing.T) {
			response := strings.Replace(clientMappingRemoteResponseJSON(), change.from, change.to, 1)
			if !json.Valid([]byte(response)) {
				t.Fatal("invalid negative fixture")
			}
			var reached atomic.Bool
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true); _, _ = w.Write([]byte(response)) }))
			defer peer.Close()
			peerNode := nodeForServer(t, peer, "pin", leafPinBase64(peer))
			peerNode.ApiToken = stored
			if result, err := NewRemote(peerNode, nil).EnrollClientMapping(context.Background(), request); err == nil || result != nil {
				t.Fatal("changed or ambiguous evidence accepted")
			}
			if !reached.Load() {
				t.Fatal("semantic negative did not reach the verified peer")
			}
		})
	}
}
