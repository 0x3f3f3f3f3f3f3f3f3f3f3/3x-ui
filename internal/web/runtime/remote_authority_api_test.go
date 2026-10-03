package runtime

import (
	"context"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// This structural interface is the existing coordinator's exact core/demand
// contract; an HTTP adapter that omits any method cannot serve that consumer.
type remoteControlDemandContract interface {
	Capabilities() *command.Capabilities
	BindAuthority(context.Context, *command.AuthorityBinding) error
	EnableAuthorityRequests(context.Context, *command.AuthorityBinding) error
	AuthorityChallenge(context.Context) (*command.AuthorityChallenge, error)
	ReadAuthorityRequests(context.Context, *command.AuthorityBinding, uint32) (*command.AuthorityRequests, error)
	InstallAuthorityGrant(context.Context, *command.ExecutionGrant) (*command.ExecutionGrantState, error)
	GetAuthorityGrant(context.Context, string, string) (*command.ExecutionGrantState, error)
	PauseAuthorityGrant(context.Context, string, string) (*command.ExecutionGrantState, error)
	SealAuthorityGrant(context.Context, string, string) (*command.ExecutionGrantState, error)
	RenewAuthorityGrant(context.Context, *command.AuthorityRenewalRequest) error
}

var _ remoteControlDemandContract = (*RemoteAuthorityAPI)(nil)

func TestRemoteAuthorityAPIPreservesBinding(t *testing.T) {
	binding, grant := controlRemoteFixture()
	var changedBoot atomic.Bool
	var changedRole atomic.Bool
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/panel/api/server/clientPolicyAuthority" {
			discovery := authorityRemoteFixture()
			discovery.ExecutionRole = func() *NodeExecutionRole { role := binding.Role(); return &role }()
			discovery.Capabilities.Capabilities = append(discovery.Capabilities.Capabilities, "on-demand-authority-requests-v1", "monotonic-grant-renewal-v1", "bounded-grant-handoff-v1")
			if changedBoot.Load() {
				discovery.Capabilities.BootId = strings.Repeat("d", 32)
				discovery.Challenge.BootId = discovery.Capabilities.BootId
			}
			if changedRole.Load() {
				discovery.ExecutionRole.Generation++
			}
			_, _ = w.Write(authorityRemoteEnvelope(t, discovery))
			return
		}
		operation := strings.TrimPrefix(r.URL.Path, "/panel/api/server/clientPolicyAuthority/")
		if operation == r.URL.Path {
			t.Error("adapter used arbitrary bind/RPC route")
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(controlRemoteResponseJSON(operation)))
	}))
	defer srv.Close()
	node := nodeForServer(t, srv, "pin", leafPinBase64(srv))
	api, err := NewRemoteAuthorityAPI(context.Background(), NewRemote(node, nil), AuthorityDiscoveryRequest{ExpectedInstanceID: binding.ExpectedInstanceID, ExpectedBootID: binding.ExpectedBootID}, binding.Role())
	if err != nil || api == nil {
		t.Fatalf("pinned typed adapter unavailable: %v", err)
	}
	node.Enable = false
	node.Address = "changed-caller-config.invalid"
	caps := api.Capabilities()
	caps.BootId = "caller-copy"
	caps.Capabilities[0] = "caller-copy"
	if api.Capabilities().BootId != binding.ExpectedBootID || api.Capabilities().Capabilities[0] == "caller-copy" {
		t.Fatal("caller changed pinned adapter metadata")
	}
	authority := &command.AuthorityBinding{AuthorityId: binding.AuthorityID, Generation: binding.Generation, NodeId: binding.NodeID}
	if err := api.BindAuthority(context.Background(), authority); err != nil {
		t.Fatalf("adapter did not retain node config: %v", err)
	}
	if err := api.EnableAuthorityRequests(context.Background(), authority); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	wrong := proto.Clone(authority).(*command.AuthorityBinding)
	wrong.Generation++
	if err := api.BindAuthority(context.Background(), wrong); err == nil || calls.Load() != before {
		t.Fatal("adapter rebound authority")
	}
	challenge, err := api.AuthorityChallenge(context.Background())
	if err != nil || challenge == nil || challenge.BootId != binding.ExpectedBootID {
		t.Fatalf("bound challenge unavailable: %v", err)
	}
	page, err := api.ReadAuthorityRequests(context.Background(), authority, 128)
	if err != nil || page == nil || page.Requests[0].ClientId != "client-a" {
		t.Fatalf("adapter demand unavailable: %v", err)
	}
	installed, err := api.InstallAuthorityGrant(context.Background(), grant)
	if err != nil || installed == nil || installed.Grant.Capacity != 9007199254740993 {
		t.Fatalf("adapter changed exact grant: %v", err)
	}
	if _, err := api.GetAuthorityGrant(context.Background(), "client-a", "grant-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PauseAuthorityGrant(context.Background(), "client-a", "grant-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.SealAuthorityGrant(context.Background(), "client-a", "grant-a"); err != nil {
		t.Fatal(err)
	}
	renewal := &command.AuthorityRenewalRequest{ExpectedBootId: binding.ExpectedBootID, ClientId: "client-a", GrantId: "grant-a", ChallengeId: strings.Repeat("b", 32), Sequence: 1, LeaseDurationMillis: 1000}
	if err := api.RenewAuthorityGrant(context.Background(), renewal); err != nil {
		t.Fatal(err)
	}
	changedBoot.Store(true)
	if challenge, err := api.AuthorityChallenge(context.Background()); err == nil || challenge != nil {
		t.Fatal("adapter accepted replacement boot")
	}
	if err := api.BindAuthority(context.Background(), authority); err == nil {
		t.Fatal("adapter updated its pinned boot")
	}
	changedBoot.Store(false)
	changedRole.Store(true)
	if challenge, err := api.AuthorityChallenge(context.Background()); err == nil || challenge != nil {
		t.Fatal("adapter accepted changed coordinator role")
	}
}
