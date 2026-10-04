package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/protobuf/proto"
)

func managedNodeTestRPC[T any, R any](r *http.Request, operation func(context.Context, T) (*R, error)) (any, error) {
	var request T
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return nil, err
	}
	return operation(r.Context(), request)
}

func TestManagedPolicyDiscoveryPinsActualInventorySourceRoleAndBoot(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc, inbound, local, _ := setupManagedActivationServiceWithUsage(t, 0, 0)
	db, ctx := database.GetDB(), context.Background()
	t.Logf("managed actual TLS discovery backend: %s", db.Dialector.Name())
	c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	id := c.state.Journal.Identity()
	node := &ClientPolicyNodeService{}
	if _, err := node.ConfigureDelegation(ctx, panelruntime.NodeDelegationRequest{AuthorityID: id.AuthorityID, Generation: id.Generation, NodeID: "managed-actual-node"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	if owner == nil || owner.controller != nil {
		t.Fatal("actual node acquired a local issuer")
	}
	caps := owner.api.Capabilities()
	var calls atomic.Int32
	var loseEnrollment atomic.Bool
	var refuseSeal atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var result any
		var err error
		switch r.URL.Path {
		case "/panel/api/server/clientPolicyAuthority":
			var request panelruntime.AuthorityDiscoveryRequest
			err = json.NewDecoder(r.Body).Decode(&request)
			if err == nil {
				result, err = node.DiscoverAuthority(r.Context(), request)
			}
		case "/panel/api/server/clientPolicyAuthority/enroll":
			var request panelruntime.NodeClientMappingRequest
			err = json.NewDecoder(r.Body).Decode(&request)
			if err == nil {
				result, err = node.EnrollClientMapping(r.Context(), request)
			}
			if err == nil && loseEnrollment.CompareAndSwap(true, false) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case "/panel/api/server/clientPolicyAuthority/requests":
			result, err = managedNodeTestRPC(r, node.ReadAuthorityRequests)
		case "/panel/api/server/clientPolicyAuthority/install":
			result, err = managedNodeTestRPC(r, node.InstallAuthorityGrant)
		case "/panel/api/server/clientPolicyAuthority/get":
			result, err = managedNodeTestRPC(r, node.GetAuthorityGrant)
		case "/panel/api/server/clientPolicyAuthority/pause":
			result, err = managedNodeTestRPC(r, node.PauseAuthorityGrant)
		case "/panel/api/server/clientPolicyAuthority/seal":
			if refuseSeal.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			result, err = managedNodeTestRPC(r, node.SealAuthorityGrant)
		case "/panel/api/server/clientPolicyAuthority/renew":
			result, err = managedNodeTestRPC(r, node.RenewAuthorityGrant)
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			t.Logf("actual managed node operation %s failed: %v", r.URL.Path, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": err == nil, "msg": "", "obj": result})
	}))
	defer srv.Close()
	address, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	cert := sha256.Sum256(srv.Certificate().Raw)
	inventory := model.Node{Name: "actual-managed-node", Scheme: "https", Address: address.Hostname(), Port: port, BasePath: "/", Enable: true, AllowPrivateAddress: true, TlsVerifyMode: "pin", PinnedCertSha256: base64.StdEncoding.EncodeToString(cert[:]), ApiToken: "managed-test-fixture-token"}
	if err := db.Create(&inventory).Error; err != nil {
		t.Fatal(err)
	}
	member := managedAuthorityMember{NodeID: "managed-actual-node", SourceID: caps.InstanceId}
	api, err := c.DiscoverNode(ctx, inventory.Id, member)
	if err != nil || api == nil || api.Capabilities().InstanceId != caps.InstanceId || api.Capabilities().BootId != caps.BootId {
		t.Fatalf("actual TLS source/boot unavailable: %v", err)
	}
	scope := model.ClientPolicyScopeGlobal
	global := model.ClientRecord{StableID: uuid.NewString(), Email: "managed-actual-global", Enable: true, TotalGB: local.TotalGB, Policy: local.Policy.Clone(), DesiredPolicyVersion: 7}
	global.Policy.Scope = &scope
	_, global.PolicyFingerprint, err = fingerprintClientPolicy(global, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&global).Error; err != nil {
		t.Fatal(err)
	}
	loseEnrollment.Store(true)
	if result, err := c.EnrollAccount(ctx, inventory.Id, global.StableID, member, local.StableID, 1); err == nil || result != nil {
		t.Fatal("lost actual enrollment reply was acknowledged")
	}
	if _, err := owner.state.Journal.LookupClientMapping(policyauthority.ClientMappingNode, member.SourceID, local.StableID); err != nil {
		t.Fatal("lost reply erased actual original node enrollment", err)
	}
	if _, err := c.state.Journal.LookupClientMapping(policyauthority.ClientMappingCoordinator, member.SourceID, local.StableID); err == nil {
		t.Fatal("lost proof invented coordinator enrollment")
	}
	proof, err := c.EnrollAccount(ctx, inventory.Id, global.StableID, member, local.StableID, 1)
	if err != nil || proof == nil || proof.Mapping.NodeAnchor != owner.state.Journal.Identity() || proof.Mapping.GlobalClientID != global.StableID || proof.Mapping.LocalClientID != local.StableID || proof.Mapping.GlobalPolicyVersion != 7 || proof.Mapping.LocalPolicyVersion != 1 {
		t.Fatalf("actual original enrollment unavailable: %+v/%v", proof, err)
	}
	retry, err := c.EnrollAccount(ctx, inventory.Id, global.StableID, member, local.StableID, 1)
	if err != nil || retry == nil || retry.Mapping != proof.Mapping {
		t.Fatal("exact enrollment retry changed original evidence", err)
	}
	var connected model.ClientPolicyCoordinatorNode
	if err := db.First(&connected, "node_id = ?", member.NodeID).Error; err != nil || connected.SourceID != member.SourceID || connected.InventoryID != inventory.Id {
		t.Fatalf("actual enrollment did not retain configured inventory connection: %+v/%v", connected, err)
	}
	account, err := c.state.Journal.Account(global.StableID)
	if err != nil || account.HeldCapacity != 0 || account.Usage != (policyauthority.Usage{}) {
		t.Fatal("enrollment fabricated grants or usage", err)
	}
	controller, err := c.ConnectNode(ctx, inventory.Id, member)
	if err != nil || controller == nil {
		t.Fatalf("actual mapped controller unavailable: %v", err)
	}
	var same *authorityController
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		same, err = c.ConnectNode(ctx, inventory.Id, member)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || same != controller {
		t.Fatal("exact live connection retry replaced its controller", err)
	}
	// A second actual local policy is enrolled after the live adapter snapshot.
	// Reconnection must refresh that snapshot before installing its real grant.
	localB := *local
	localB.Id, localB.StableID, localB.Email = 0, uuid.NewString(), "managed-actual-local-b"
	localB.DesiredPolicyVersion = 1
	_, localB.PolicyFingerprint, err = fingerprintClientPolicy(localB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&localB).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicyLedger(caps.InstanceId, localB.StableID); err != nil {
		t.Fatal(err)
	}
	actualLocal, err := owner.api.GetClient(ctx, local.StableID)
	if err != nil {
		t.Fatal(err)
	}
	policyB := proto.Clone(actualLocal.Policy).(*clientpolicy.PolicyConfig)
	policyB.ClientId = localB.StableID
	if err := owner.api.Apply(ctx, []*clientpolicy.PolicyConfig{policyB}); err != nil {
		t.Fatal(err)
	}
	localAccount, err := owner.state.Journal.Account(local.StableID)
	if err != nil {
		t.Fatal(err)
	}
	seedB := localAccount.Seed
	seedB.ClientID = localB.StableID
	if err := owner.state.Journal.AddAccount(seedB); err != nil {
		t.Fatal(err)
	}
	globalB := global
	globalB.Id, globalB.StableID, globalB.Email = 0, uuid.NewString(), "managed-actual-global-b"
	_, globalB.PolicyFingerprint, err = fingerprintClientPolicy(globalB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&globalB).Error; err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		_, err = c.EnrollAccount(ctx, inventory.Id, globalB.StableID, member, localB.StableID, 1)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("second actual account enrollment failed", err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		same, err = c.ConnectNode(ctx, inventory.Id, member)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || same == controller {
		t.Fatal("new original enrollment retained stale live adapter", err)
	}
	controller = same
	var grantB policyauthority.Grant
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		grantB, err = controller.execution.Authorize(ctx, authorityAllocation{ClientID: globalB.StableID, RequestID: "0123456789abcdef0123456789abcdef", Capacity: 16, Upload: policyauthority.Direction{Unlimited: true}, Download: policyauthority.Direction{Unlimited: true}, LeaseDuration: time.Second})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("refreshed second account could not install actual mapped grant", err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		err = controller.execution.Settle(ctx, grantB.GrantID, true, false)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readAuthorityManifest(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	tampered := manifest
	tampered.Identity.Generation++
	if err := writeAuthorityManifest(c.dir, tampered, false); err != nil {
		t.Fatal(err)
	}
	guardErr := controller.api.(*managedNodeDemandAPI).admit(ctx)
	if err := writeAuthorityManifest(c.dir, manifest, false); err != nil {
		t.Fatal(err)
	}
	if guardErr == nil {
		t.Fatal("live node admission ignored replaced original coordinator manifest")
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if err := flow.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Write([]byte("live")); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4)
	if _, err := io.ReadFull(flow, payload); err != nil || string(payload) != "live" {
		t.Fatalf("actual mapped controller did not authorize Tunnel payload: %q/%v", payload, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	// Lose the process-local controller after traffic without sealing its
	// original allocation. Reopening must reconcile the same actual core boot.
	if err := controller.join(ctx); err != nil {
		t.Fatal(err)
	}
	beforeRecovery, err := c.state.Journal.Account(global.StableID)
	if err != nil || beforeRecovery.HeldCapacity == 0 {
		t.Fatalf("unsealed actual allocation disappeared: %+v/%v", beforeRecovery, err)
	}
	originalIdentity, originalDir := c.state.Journal.Identity(), c.dir
	if err := c.state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	c.closed = true
	c, err = openManagedPolicyCoordinator(ctx, db, originalDir)
	if err != nil || c.state.Journal.Identity() != originalIdentity {
		t.Fatal("coordinator reopen replaced original authority", err)
	}
	refuseSeal.Store(true)
	if failed, err := c.ConnectNode(ctx, inventory.Id, member); err == nil || failed != nil {
		t.Fatal("unreachable seal invented recovery")
	}
	retained, err := c.state.Journal.Account(global.StableID)
	if err != nil || retained != beforeRecovery {
		t.Fatalf("failed authentic recovery changed original allowance: %+v/%v", retained, err)
	}
	refuseSeal.Store(false)
	controller, err = c.ConnectNode(ctx, inventory.Id, member)
	if err != nil || controller == nil {
		t.Fatal("actual same-boot recovery failed", err)
	}
	if err := controller.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	account, err = c.state.Journal.Account(global.StableID)
	if err != nil || account.Usage.RawUpload != 4 || account.Usage.RawDownload != 4 || account.Usage.BilledBytes != 16 || account.HeldCapacity != 0 {
		t.Fatalf("actual mapped settlement lost exact2x usage: %+v/%v", account, err)
	}
	if err := c.ResumeNodes(ctx); err != nil || c.controllers[member.NodeID] == controller {
		t.Fatal("configured startup did not reconnect actual original node", err)
	}
	controller = c.controllers[member.NodeID]
	second, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := second.Write([]byte("boot")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(second, payload); err != nil || string(payload) != "boot" {
		t.Fatalf("actual pre-restart payload unavailable: %q/%v", payload, err)
	}
	_ = second.Close()
	if err := controller.join(ctx); err != nil {
		t.Fatal(err)
	}
	beforeBoot, err := c.state.Journal.Account(global.StableID)
	if err != nil || beforeBoot.HeldCapacity == 0 {
		t.Fatal("node restart fixture lost its actual held grant", err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	freshOwner := managedAuthorityForProcess(currentXrayProcess())
	if freshOwner == nil || freshOwner.api.Capabilities().InstanceId != member.SourceID || freshOwner.api.Capabilities().BootId == caps.BootId {
		t.Fatal("actual core restart did not change only its boot")
	}
	freshController, err := c.ConnectNode(ctx, inventory.Id, member)
	if err != nil || freshController == nil || freshController == controller {
		t.Fatalf("fresh actual boot could not reconnect conservatively: %v", err)
	}
	afterBoot, err := c.state.Journal.Account(global.StableID)
	if err != nil || afterBoot != beforeBoot {
		t.Fatalf("fresh boot replenished or invented old receipt: %+v/%v", afterBoot, err)
	}
	if err := freshController.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []managedAuthorityMember{{NodeID: "other-role", SourceID: member.SourceID}, {NodeID: member.NodeID, SourceID: "other-source"}} {
		if api, err := c.DiscoverNode(ctx, inventory.Id, wrong); err == nil || api != nil {
			t.Fatal("actual source/role replacement accepted")
		}
	}
	if err := db.Model(&inventory).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if api, err := c.DiscoverNode(ctx, inventory.Id, member); err == nil || api != nil || calls.Load() != before {
		t.Fatal("disabled inventory reached peer")
	}
	if err := c.ResumeNodes(ctx); err != nil || len(c.controllers) != 0 {
		t.Fatal("disabled member retained an issuing controller", err)
	}
	if _, err := c.DiscoverNode(nil, inventory.Id, member); err == nil {
		t.Fatal("nil context admitted discovery")
	}
}
