package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
)

// These tests catch omission of owned admission or substitution of a local
// allocator: every successful grant originates in an independent journal.
type nodeControlFixture struct {
	svc     *XrayService
	node    *ClientPolicyNodeService
	owner   *managedAuthority
	client  *model.ClientRecord
	inbound *model.Inbound
	journal *policyauthority.Journal
	binding panelruntime.NodeAuthorityControlBinding
	grant   *command.ExecutionGrant
}

func newNodeControlFixture(t *testing.T) *nodeControlFixture {
	t.Helper()
	if os.Getenv("XRAY_E2E_BINARY") == "" {
		t.Skip("actual core required for node authority control acceptance")
	}
	setupPolicyLedgerDB(t)
	svc, inbound, client, _ := setupManagedActivationServiceWithUsage(t, 0, 0)
	journal := createServiceFixtureGrantJournal(t, nil)
	id := journal.Identity()
	node := &ClientPolicyNodeService{}
	role := panelruntime.NodeDelegationRequest{AuthorityID: id.AuthorityID, Generation: id.Generation, NodeID: "controlled-node-a"}
	if _, err := node.ConfigureDelegation(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	if owner == nil || owner.controller != nil {
		t.Fatal("fixture acquired a local issuer")
	}
	account, err := owner.state.Journal.Account(client.StableID)
	if err != nil || account.Seed.Usage != (policyauthority.Usage{}) || account.HeldCapacity != 0 {
		t.Fatalf("fixture is not fresh: %v", err)
	}
	if err := journal.AddAccount(account.Seed); err != nil {
		t.Fatal(err)
	}
	caps := owner.api.Capabilities()
	boot := policyauthority.NodeBoot{NodeID: role.NodeID, SourceID: caps.InstanceId, BootID: caps.BootId}
	if err := journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	challenge, err := owner.api.AuthorityChallenge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	issued, err := journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: id, NodeBoot: boot, ClientID: client.StableID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "control-test-allocation", ChallengeID: challenge.ChallengeId, Capacity: 256, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return &nodeControlFixture{svc: svc, node: node, owner: owner, client: client, inbound: inbound, journal: journal, binding: panelruntime.NodeAuthorityControlBinding{ExpectedInstanceID: caps.InstanceId, ExpectedBootID: caps.BootId, AuthorityID: id.AuthorityID, Generation: id.Generation, NodeID: role.NodeID}, grant: executionGrantFromAuthority(issued)}
}

func (f *nodeControlFixture) operations(binding panelruntime.NodeAuthorityControlBinding) []struct {
	name string
	call func(context.Context) (bool, error)
} {
	request := panelruntime.NodeAuthorityGrantRequest{Binding: binding, ClientID: f.client.StableID, GrantID: f.grant.GrantId}
	return []struct {
		name string
		call func(context.Context) (bool, error)
	}{
		{"requests", func(ctx context.Context) (bool, error) {
			r, e := f.node.ReadAuthorityRequests(ctx, panelruntime.NodeAuthorityRequestsRequest{Binding: binding, Limit: 128})
			return r != nil, e
		}},
		{"install", func(ctx context.Context) (bool, error) {
			r, e := f.node.InstallAuthorityGrant(ctx, panelruntime.NodeAuthorityInstallRequest{Binding: binding, Grant: f.grant})
			return r != nil, e
		}},
		{"get", func(ctx context.Context) (bool, error) {
			r, e := f.node.GetAuthorityGrant(ctx, request)
			return r != nil, e
		}},
		{"pause", func(ctx context.Context) (bool, error) {
			r, e := f.node.PauseAuthorityGrant(ctx, request)
			return r != nil, e
		}},
		{"seal", func(ctx context.Context) (bool, error) {
			r, e := f.node.SealAuthorityGrant(ctx, request)
			return r != nil, e
		}},
		{"renew", func(ctx context.Context) (bool, error) {
			r, e := f.node.RenewAuthorityGrant(ctx, panelruntime.NodeAuthorityRenewalRequest{Binding: binding, Renewal: &command.AuthorityRenewalRequest{ExpectedBootId: f.binding.ExpectedBootID, ClientId: f.client.StableID, GrantId: f.grant.GrantId, ChallengeId: f.grant.ChallengeId, Sequence: 1, LeaseDurationMillis: 10000}})
			return r != nil, e
		}},
	}
}

func assertNodeControlRefuses(t *testing.T, f *nodeControlFixture, ctx context.Context, binding panelruntime.NodeAuthorityControlBinding) {
	t.Helper()
	for _, operation := range f.operations(binding) {
		t.Run(operation.name, func(t *testing.T) {
			if hasResult, err := operation.call(ctx); err == nil || hasResult {
				t.Fatal("unsafe node authority operation succeeded")
			}
		})
	}
}

func TestNodeAuthorityControlRequiresOwnedDelegatedCore(t *testing.T) {
	t.Run("delegated-owned", func(t *testing.T) {
		f := newNodeControlFixture(t)
		for _, tc := range []struct {
			name    string
			ctx     context.Context
			prepare func() func()
		}{
			{"nil-context", nil, nil},
			{"canceled", func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }(), nil},
			{"lifecycle-busy", context.Background(), func() func() { lock.Lock(); return lock.Unlock }},
			{"owner-busy", context.Background(), func() func() { f.owner.mu.Lock(); return f.owner.mu.Unlock }},
			{"restore-admission", context.Background(), func() func() {
				lease, err := database.BeginRestore()
				if err != nil {
					t.Fatal(err)
				}
				return lease.Close
			}},
			{"foreign-process", context.Background(), func() func() { return SetXrayProcessForTest(nil) }},
			{"replaced-socket", context.Background(), func() func() {
				old := f.owner.socketPath + ".retained-control"
				if err := os.Rename(f.owner.socketPath, old); err != nil {
					t.Fatal(err)
				}
				listener, err := net.Listen("unix", f.owner.socketPath)
				if err != nil {
					t.Fatal(err)
				}
				return func() {
					_ = listener.Close()
					if err := os.Rename(old, f.owner.socketPath); err != nil {
						t.Fatal(err)
					}
				}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if tc.prepare != nil {
					restore := tc.prepare()
					defer restore()
				}
				assertNodeControlRefuses(t, f, tc.ctx, f.binding)
			})
		}
		flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", f.inbound.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer flow.Close()
		if _, err := flow.Write([]byte("held")); err != nil {
			t.Fatal(err)
		}
		page, err := f.node.ReadAuthorityRequests(context.Background(), panelruntime.NodeAuthorityRequestsRequest{Binding: f.binding, Limit: 128})
		if err != nil || page == nil || page.Requests == nil || len(page.Requests.Requests) != 1 || page.Requests.Requests[0].ClientId != f.client.StableID || page.InstanceID != f.binding.ExpectedInstanceID || page.BootID != f.binding.ExpectedBootID || page.ExecutionRole.AuthorityID != f.binding.AuthorityID {
			t.Fatalf("owned delegated demand unavailable: %+v/%v", page, err)
		}
		page.ExecutionRole.AuthorityID = "caller-only"
		if f.owner.state.Role.AuthorityID != f.binding.AuthorityID {
			t.Fatal("response changed owned role")
		}
		if err := f.svc.StopXray(); err != nil {
			t.Fatal(err)
		}
		assertNodeControlRefuses(t, f, context.Background(), f.binding)
	})
	t.Run("local-owner", func(t *testing.T) {
		svc, _, client, _ := setupManagedActivationService(t)
		if err := svc.RestartXray(true); err != nil {
			t.Fatal(err)
		}
		owner := managedAuthorityForProcess(currentXrayProcess())
		caps := owner.api.Capabilities()
		f := &nodeControlFixture{svc: svc, node: &ClientPolicyNodeService{}, owner: owner, client: client, binding: panelruntime.NodeAuthorityControlBinding{ExpectedInstanceID: caps.InstanceId, ExpectedBootID: caps.BootId, AuthorityID: "foreign-coordinator", Generation: 1, NodeID: "foreign-node"}, grant: &command.ExecutionGrant{ClientId: client.StableID, GrantId: "foreign-grant"}}
		assertNodeControlRefuses(t, f, context.Background(), f.binding)
		if owner.controller == nil || !currentXrayProcess().IsRunning() {
			t.Fatal("refused remote control interrupted local issuer")
		}
	})
}

func TestNodeAuthorityControlBindsEveryOperation(t *testing.T) {
	f := newNodeControlFixture(t)
	installed, err := f.node.InstallAuthorityGrant(context.Background(), panelruntime.NodeAuthorityInstallRequest{Binding: f.binding, Grant: f.grant})
	if err != nil || installed == nil || installed.State == nil || !proto.Equal(installed.State.Grant, f.grant) || installed.ExecutionRole != f.owner.state.Role {
		t.Fatalf("independent coordinator grant unavailable: %+v/%v", installed, err)
	}
	for _, name := range []string{"source", "boot", "authority", "generation", "node"} {
		t.Run(name, func(t *testing.T) {
			binding := f.binding
			switch name {
			case "source":
				binding.ExpectedInstanceID = "different-source"
			case "boot":
				binding.ExpectedBootID = "dddddddddddddddddddddddddddddddd"
			case "authority":
				binding.AuthorityID = "different-authority"
			case "generation":
				binding.Generation++
			case "node":
				binding.NodeID = "different-node"
			}
			assertNodeControlRefuses(t, f, context.Background(), binding)
		})
	}
	for _, name := range []string{"inner-authority", "inner-source", "inner-boot", "nil-share", "zero-capacity", "oversize-duration"} {
		t.Run(name, func(t *testing.T) {
			grant := proto.Clone(f.grant).(*command.ExecutionGrant)
			switch name {
			case "inner-authority":
				grant.Authority.AuthorityId = "different-authority"
			case "inner-source":
				grant.InstanceId = "different-source"
			case "inner-boot":
				grant.BootId = "dddddddddddddddddddddddddddddddd"
			case "nil-share":
				grant.Upload = nil
			case "zero-capacity":
				grant.Capacity = 0
			case "oversize-duration":
				grant.LeaseDurationMillis = 10001
			}
			if result, err := f.node.InstallAuthorityGrant(context.Background(), panelruntime.NodeAuthorityInstallRequest{Binding: f.binding, Grant: grant}); err == nil || result != nil {
				t.Fatal("invalid inner grant succeeded")
			}
		})
	}
	for _, limit := range []uint32{0, 129} {
		if result, err := f.node.ReadAuthorityRequests(context.Background(), panelruntime.NodeAuthorityRequestsRequest{Binding: f.binding, Limit: limit}); err == nil || result != nil {
			t.Fatal("unbounded demand request succeeded")
		}
	}
	request := panelruntime.NodeAuthorityGrantRequest{Binding: f.binding, ClientID: f.client.StableID, GrantID: f.grant.GrantId}
	state, err := f.node.GetAuthorityGrant(context.Background(), request)
	if err != nil || state == nil || !proto.Equal(state.State.Grant, f.grant) || state.State.Sealed {
		t.Fatalf("unsafe requests changed actual grant: %+v/%v", state, err)
	}
	installed.State.Grant.Authority.AuthorityId = "response-only"
	actual, err := f.owner.api.GetAuthorityGrant(context.Background(), f.client.StableID, f.grant.GrantId)
	if err != nil || !proto.Equal(actual.Grant, f.grant) {
		t.Fatal("caller changed actual installed grant")
	}
	challenge, err := f.owner.api.AuthorityChallenge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	renewal := &command.AuthorityRenewalRequest{ExpectedBootId: f.binding.ExpectedBootID, ClientId: f.client.StableID, GrantId: f.grant.GrantId, ChallengeId: challenge.ChallengeId, Sequence: 1, LeaseDurationMillis: 10000}
	for _, name := range []string{"wrong-inner-boot", "missing-client", "missing-grant", "missing-challenge", "zero-sequence", "oversize-duration"} {
		t.Run(name, func(t *testing.T) {
			bad := proto.Clone(renewal).(*command.AuthorityRenewalRequest)
			switch name {
			case "wrong-inner-boot":
				bad.ExpectedBootId = "dddddddddddddddddddddddddddddddd"
			case "missing-client":
				bad.ClientId = ""
			case "missing-grant":
				bad.GrantId = ""
			case "missing-challenge":
				bad.ChallengeId = ""
			case "zero-sequence":
				bad.Sequence = 0
			case "oversize-duration":
				bad.LeaseDurationMillis = 10001
			}
			if result, err := f.node.RenewAuthorityGrant(context.Background(), panelruntime.NodeAuthorityRenewalRequest{Binding: f.binding, Renewal: bad}); err == nil || result != nil {
				t.Fatal("invalid inner renewal succeeded")
			}
		})
	}
	ack, err := f.node.RenewAuthorityGrant(context.Background(), panelruntime.NodeAuthorityRenewalRequest{Binding: f.binding, Renewal: renewal})
	if err != nil || ack == nil || !proto.Equal(ack.Renewal, renewal) {
		t.Fatalf("real grant renewal unavailable: %+v/%v", ack, err)
	}
	ack.Renewal.Sequence = 9
	if renewal.Sequence != 1 {
		t.Fatal("renewal acknowledgement aliases caller input")
	}
	paused, err := f.node.PauseAuthorityGrant(context.Background(), request)
	if err != nil || paused == nil || !paused.State.Sealed {
		t.Fatalf("real grant pause unavailable: %+v/%v", paused, err)
	}
	sealed, err := f.node.SealAuthorityGrant(context.Background(), request)
	if err != nil || sealed == nil || !sealed.State.Sealed {
		t.Fatalf("real grant seal unavailable: %+v/%v", sealed, err)
	}
	if account, err := f.journal.Account(f.client.StableID); err != nil || account.HeldCapacity != 256 {
		t.Fatalf("node response released unreported coordinator allowance: %+v/%v", account, err)
	}
	if account, err := f.owner.state.Journal.Account(f.client.StableID); err != nil || account.HeldCapacity != 0 {
		t.Fatalf("node issued a local grant: %+v/%v", account, err)
	}
	if err := f.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	assertNodeControlRefuses(t, f, context.Background(), f.binding)
	fresh := managedAuthorityForProcess(currentXrayProcess())
	if fresh == nil || fresh.controller != nil || fresh.api.Capabilities().BootId == f.binding.ExpectedBootID || fresh.state.Role != f.binding.Role() {
		t.Fatal("actual restart lost delegated binding or fresh boot")
	}
}
