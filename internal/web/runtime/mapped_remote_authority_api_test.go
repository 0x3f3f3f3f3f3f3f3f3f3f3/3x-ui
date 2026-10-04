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

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
)

var _ remoteControlDemandContract = (*MappedRemoteAuthorityAPI)(nil)

func TestMappedRemoteAuthorityAPITranslatesCanonicalIdentity(t *testing.T) {
	r := clientMappingRemoteRequest()
	var proof NodeClientMappingResult
	var envelope struct {
		Obj json.RawMessage `json:"obj"`
	}
	if json.Unmarshal([]byte(clientMappingRemoteResponseJSON()), &envelope) != nil || json.Unmarshal(envelope.Obj, &proof) != nil {
		t.Fatal("invalid literal mapping evidence")
	}
	var calls atomic.Int32
	var staleBoot, changedVersion atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.URL.Path == "/panel/api/server/clientPolicyAuthority" {
			discovery := authorityRemoteFixture()
			role := r.Binding.Role()
			discovery.ExecutionRole = &role
			discovery.Capabilities.Capabilities = append(discovery.Capabilities.Capabilities, "on-demand-authority-requests-v1", "monotonic-grant-renewal-v1", "bounded-grant-handoff-v1", "client-authority-history-v1")
			if staleBoot.Load() {
				discovery.Capabilities.BootId = strings.Repeat("d", 32)
				discovery.Challenge.BootId = discovery.Capabilities.BootId
			}
			_, _ = w.Write(authorityRemoteEnvelope(t, discovery))
			return
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		op := strings.TrimPrefix(req.URL.Path, "/panel/api/server/clientPolicyAuthority/")
		switch op {
		case "requests":
			var wire NodeAuthorityRequestsRequest
			if json.Unmarshal(raw, &wire) != nil || wire.Binding != r.Binding || wire.Limit != 128 {
				t.Error("mapped demand did not fetch complete bounded128 before filtering")
				w.WriteHeader(400)
				return
			}
			response := `{"success":true,"obj":{` + controlRemoteIdentityJSON() + `,"requests":{"instanceId":"private-node-instance","bootId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","requests":[{"requestId":"dddddddddddddddddddddddddddddddd","clientId":"00000000-0000-4000-8000-000000000001","policyVersion":"1","previousGrantId":""},{"requestId":"cccccccccccccccccccccccccccccccc","clientId":"22222222-2222-4222-8222-222222222222","policyVersion":"1","previousGrantId":"prior-a"}]}}}`
			if changedVersion.Load() {
				response = strings.ReplaceAll(response, `"policyVersion":"1"`, `"policyVersion":"2"`)
			}
			_, _ = w.Write([]byte(response))
			return
		case "install":
			var wire NodeAuthorityInstallRequest
			if json.Unmarshal(raw, &wire) != nil || wire.Binding != r.Binding || wire.Grant.ClientId != r.LocalClientID || wire.Grant.PolicyVersion != 1 || wire.Grant.Capacity != 9007199254740993 {
				t.Error("grant wire lost local identity/version or exact capacity")
				w.WriteHeader(400)
				return
			}
		case "renew":
			var wire NodeAuthorityRenewalRequest
			if json.Unmarshal(raw, &wire) != nil || wire.Binding != r.Binding || wire.Renewal.ClientId != r.LocalClientID {
				t.Error("renewal wire lost local identity")
				w.WriteHeader(400)
				return
			}
		default:
			var wire NodeAuthorityGrantRequest
			if json.Unmarshal(raw, &wire) != nil || wire.Binding != r.Binding || wire.ClientID != r.LocalClientID || wire.GrantID != "grant-a" {
				t.Error("grant query wire used canonical identity")
				w.WriteHeader(400)
				return
			}
		}
		response := strings.ReplaceAll(controlRemoteResponseJSON(op), `"client-a"`, `"`+r.LocalClientID+`"`)
		response = strings.Replace(response, `"usage":{"rawUpload":"0","rawDownload":"0","billedBytes":"0","remainder":"0"},"sequence":"1"`, `"usage":{"rawUpload":"3","rawDownload":"4","billedBytes":"14","remainder":"4321"},"sequence":"9"`, 1)
		if changedVersion.Load() {
			response = strings.ReplaceAll(response, `"policyVersion":"1"`, `"policyVersion":"2"`)
		}
		_, _ = w.Write([]byte(response))
	}))
	defer srv.Close()
	pinned, err := NewRemoteAuthorityAPI(context.Background(), NewRemote(nodeForServer(t, srv, "pin", leafPinBase64(srv)), nil), AuthorityDiscoveryRequest{ExpectedInstanceID: r.Binding.ExpectedInstanceID, ExpectedBootID: r.Binding.ExpectedBootID}, r.Binding.Role())
	if err != nil {
		t.Fatal(err)
	}
	mappings := []policyauthority.ClientMapping{proof.Mapping}
	api, err := NewMappedRemoteAuthorityAPI(pinned, mappings)
	if err != nil || api == nil {
		t.Fatalf("canonical/local mapping adapter unavailable: %v", err)
	}
	pinnedCopy := *pinned
	*pinned = RemoteAuthorityAPI{}
	capabilities := api.Capabilities()
	capabilities.BootId = "changed-caller-copy"
	if api.Capabilities().BootId != r.Binding.ExpectedBootID {
		t.Fatal("caller changed mapped capability snapshot")
	}
	mappings[0].LocalClientID = "33333333-3333-4333-8333-333333333333"
	authority := &command.AuthorityBinding{AuthorityId: r.Binding.AuthorityID, Generation: r.Binding.Generation, NodeId: r.Binding.NodeID}
	page, err := api.ReadAuthorityRequests(context.Background(), authority, 1)
	if err != nil || page == nil || len(page.Requests) != 1 || page.Requests[0].ClientId != r.GlobalClientID || page.Requests[0].PolicyVersion != 7 || page.Requests[0].PreviousGrantId != "prior-a" {
		t.Fatalf("unknown sorted demand starved or corrupted mapped client: %+v/%v", page, err)
	}
	_, grant := controlRemoteFixture()
	grant.ClientId = r.GlobalClientID
	grant.PolicyVersion = 7
	original := proto.Clone(grant)
	state, err := api.InstallAuthorityGrant(context.Background(), grant)
	if err != nil || state == nil || !proto.Equal(state.Grant, grant) || !proto.Equal(original, grant) || state.Usage.BilledBytes != 14 || state.Usage.Remainder != 4321 || state.Sequence != 9 {
		t.Fatalf("mapped install changed billing/grant or caller ownership: %+v/%v", state, err)
	}
	for _, op := range []string{"get", "pause", "seal"} {
		t.Run(op, func(t *testing.T) {
			var result *command.ExecutionGrantState
			var err error
			switch op {
			case "get":
				result, err = api.GetAuthorityGrant(context.Background(), r.GlobalClientID, "grant-a")
			case "pause":
				result, err = api.PauseAuthorityGrant(context.Background(), r.GlobalClientID, "grant-a")
			case "seal":
				result, err = api.SealAuthorityGrant(context.Background(), r.GlobalClientID, "grant-a")
			}
			if err != nil || result == nil || !proto.Equal(result.Grant, grant) || result.Usage.RawUpload != 3 || result.Usage.RawDownload != 4 || result.Usage.BilledBytes != 14 || result.Usage.Remainder != 4321 || result.Sequence != 9 || result.Sealed != (op != "get") {
				t.Fatalf("translated cumulative state changed: %+v/%v", result, err)
			}
		})
	}
	renewal := &command.AuthorityRenewalRequest{ExpectedBootId: r.Binding.ExpectedBootID, ClientId: r.GlobalClientID, GrantId: "grant-a", ChallengeId: strings.Repeat("b", 32), Sequence: 1, LeaseDurationMillis: 1000}
	beforeRenewal := proto.Clone(renewal)
	if err := api.RenewAuthorityGrant(context.Background(), renewal); err != nil || !proto.Equal(beforeRenewal, renewal) {
		t.Fatal("renewal did not preserve caller and canonical identity", err)
	}
	unknown := proto.Clone(grant).(*command.ExecutionGrant)
	unknown.ClientId = "33333333-3333-4333-8333-333333333333"
	future := proto.Clone(grant).(*command.ExecutionGrant)
	future.PolicyVersion++
	for _, bad := range []*command.ExecutionGrant{nil, unknown, future} {
		before := calls.Load()
		if result, err := api.InstallAuthorityGrant(context.Background(), bad); err == nil || result != nil || calls.Load() != before {
			t.Fatal("unknown/stale canonical policy reached peer")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		before := calls.Load()
		if result, err := api.GetAuthorityGrant(ctx, r.GlobalClientID, "grant-a"); err == nil || result != nil || calls.Load() != before {
			t.Fatal("invalid context reached peer")
		}
	}
	for _, limit := range []uint32{0, 129} {
		before := calls.Load()
		if result, err := api.ReadAuthorityRequests(context.Background(), authority, limit); err == nil || result != nil || calls.Load() != before {
			t.Fatal("unbounded page reached peer")
		}
	}
	changedVersion.Store(true)
	if result, err := api.ReadAuthorityRequests(context.Background(), authority, 1); err == nil || result != nil {
		t.Fatal("changed local demand version accepted")
	}
	if result, err := api.GetAuthorityGrant(context.Background(), r.GlobalClientID, "grant-a"); err == nil || result != nil {
		t.Fatal("changed local grant version accepted")
	}
	changedVersion.Store(false)
	staleBoot.Store(true)
	if err := api.BindAuthority(context.Background(), authority); err == nil {
		t.Fatal("mapped adapter accepted replacement boot")
	}
	staleBoot.Store(false)
	*pinned = pinnedCopy
	for _, field := range []string{"source", "node", "authority", "generation", "anchor", "collision"} {
		bad := proof.Mapping
		switch field {
		case "source":
			bad.SourceID = "foreign-source"
		case "node":
			bad.NodeID = "foreign-node"
		case "authority":
			bad.Authority.AuthorityID = "foreign-coordinator"
		case "generation":
			bad.Authority.Generation++
		case "anchor":
			bad.NodeAnchor.Generation = 0
		case "collision":
			bad.GlobalClientID = "33333333-3333-4333-8333-333333333333"
		}
		inputs := []policyauthority.ClientMapping{bad}
		if field == "collision" {
			inputs = append(inputs, proof.Mapping)
		}
		if result, err := NewMappedRemoteAuthorityAPI(pinned, inputs); err == nil || result != nil {
			t.Fatal("invalid mapping tuple admitted", field)
		}
	}
}
