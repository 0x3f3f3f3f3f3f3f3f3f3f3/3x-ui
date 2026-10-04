package sub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
)

func TestNodeClientMappingHTTPActualCanonicalGrant(t *testing.T) {
	t.Run("canonical-grant", testNodeClientMappingCanonicalGrant)
	t.Run("actual-local-issuer-refuses", testNodeClientMappingLocalIssuer)
}

func testNodeClientMappingCanonicalGrant(t *testing.T) {
	h := newNativeHTTPHarness(t, "node_mapping_http")
	t.Logf("node client mapping canonical HTTP backend: %s", database.GetDB().Dialector.Name())
	tunnel, local := nodeControlHTTPClient(t, h)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dir, "coordinator.db")
	journal, identity, err := policyauthority.Create(journalPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	peer := newNodeControlHTTPPeer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	role := panelruntime.NodeDelegationRequest{AuthorityID: identity.AuthorityID, Generation: identity.Generation, NodeID: "mapped-node-a"}
	if _, err := peer.remote.ConfigureDelegation(ctx, role); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	found, err := peer.remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	bound := panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: found.Capabilities.InstanceId, ExpectedBootID: found.Capabilities.BootId}
	pinned, err := panelruntime.NewRemoteAuthorityAPI(ctx, peer.remote, bound, role.Role())
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := h.svc.GetXrayAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := xray.DialClientPolicy(ctx, endpoint, bound.ExpectedInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer actual.Close()
	state, err := actual.GetClient(ctx, local.StableID)
	if err != nil || state == nil || state.Policy == nil || state.Usage == nil || state.AuthorityGrantHistory || !proto.Equal(state.Usage, &command.Usage{}) || state.Policy.Version != 1 || state.Policy.MultiplierMicros != 2000000 {
		t.Fatalf("actual fresh local1/2x proof unavailable: %+v/%v", state, err)
	}
	digest, err := panelruntime.EffectiveClientPolicyDigest(state.Policy)
	if err != nil {
		t.Fatal(err)
	}
	global := model.ClientRecord{StableID: "11111111-1111-4111-8111-111111111111", Email: "canonical-global-owner", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if global.StableID == local.StableID {
		t.Fatal("fixture needs distinct stable UUIDs")
	}
	if err := database.GetDB().Create(&global).Error; err != nil {
		t.Fatal(err)
	}
	// Real desired-policy preparation advances the independent canonical epoch.
	for _, quota := range []int64{10000, 10001, 10002, 10003, 10004, 10005, 10000} {
		if err := database.GetDB().Table("clients").Where("stable_id = ?", global.StableID).Update("total_gb", quota).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := service.PrepareClientPolicies([]string{global.StableID}); err != nil {
			t.Fatal(err)
		}
	}
	var canonical model.ClientRecord
	if err := database.GetDB().Where("stable_id = ?", global.StableID).First(&canonical).Error; err != nil || canonical.DesiredPolicyVersion != 7 {
		t.Fatal("canonical epoch7 not independently prepared", err)
	}
	policy := policyauthority.Policy{WindowID: "global-window-7", Version: 7, QuotaBytes: 10000, Upload: policyauthority.Direction{Unlimited: true}, Download: policyauthority.Direction{Unlimited: true}}
	if err := journal.AddAccount(policyauthority.Seed{ClientID: global.StableID, Policy: policy}); err != nil {
		t.Fatal(err)
	}
	request := panelruntime.NodeClientMappingRequest{Binding: panelruntime.NodeAuthorityControlBinding{ExpectedInstanceID: bound.ExpectedInstanceID, ExpectedBootID: bound.ExpectedBootID, AuthorityID: identity.AuthorityID, Generation: identity.Generation, NodeID: role.NodeID}, GlobalClientID: global.StableID, LocalClientID: local.StableID, GlobalPolicyVersion: 7, LocalPolicyVersion: 1, ExpectedPolicyDigest: digest}
	coordinator, err := service.NewClientPolicyNodeMappingService(database.GetDB(), journal)
	if err != nil {
		t.Fatal(err)
	}
	peer.loseEnroll.Store(true)
	if proof, err := coordinator.Enroll(ctx, pinned, request); err == nil || proof != nil || !peer.lostEnrollSuccess.Load() {
		t.Fatal("lost successful production enrollment was acknowledged or not actually committed")
	}
	if _, err := journal.LookupClientMapping(policyauthority.ClientMappingCoordinator, bound.ExpectedInstanceID, local.StableID); err != policyauthority.ErrNotFound {
		t.Fatal("lost node reply fabricated coordinator evidence", err)
	}
	proof, err := coordinator.Enroll(ctx, pinned, request)
	if err != nil || proof.Validate(request) != nil {
		t.Fatalf("exact lost enrollment recovery failed: %v", err)
	}
	manifestPath := filepath.Join(config.GetDBFolderPath(), "client-policy", "authority", "authority.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Identity policyauthority.Identity `json:"identity"`
		SourceID string                   `json:"sourceId"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Identity != proof.Mapping.NodeAnchor || manifest.SourceID != proof.Mapping.SourceID || proof.Mapping.Authority != identity || proof.Mapping.NodeAnchor == identity {
		t.Fatal("original node/coordinator anchors were conflated")
	}
	if err := database.GetDB().Migrator().DropTable(&model.ClientPolicyNodeMapping{}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.AuthorityAPI(ctx, pinned); err != nil {
		t.Fatal("projection deletion replaced journal authority", err)
	}
	if err := database.GetDB().AutoMigrate(&model.ClientPolicyNodeMapping{}); err != nil {
		t.Fatal(err)
	}
	restored, err := coordinator.Enroll(ctx, pinned, request)
	if err != nil || restored.Mapping != proof.Mapping {
		t.Fatal("empty restored projection retargeted binding", err)
	}
	conflict := request
	conflict.GlobalClientID = "33333333-3333-4333-8333-333333333333"
	if result, err := peer.remote.EnrollClientMapping(ctx, conflict); err == nil || result != nil {
		t.Fatal("original node mapping retargeted")
	}
	adapter, err := coordinator.AuthorityAPI(ctx, pinned)
	if err != nil {
		t.Fatal(err)
	}
	boot := policyauthority.NodeBoot{NodeID: role.NodeID, SourceID: bound.ExpectedInstanceID, BootID: bound.ExpectedBootID}
	if err := journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	binding := &command.AuthorityBinding{AuthorityId: identity.AuthorityID, Generation: identity.Generation, NodeId: role.NodeID}
	nodeControlHTTPNoBytes(t, tunnel)
	demand, err := adapter.ReadAuthorityRequests(ctx, binding, 128)
	if err != nil || demand == nil || len(demand.Requests) != 1 || demand.Requests[0].ClientId != global.StableID || demand.Requests[0].PolicyVersion != 7 {
		t.Fatal("actual local demand not canonicalized", err)
	}
	time.Sleep(2100 * time.Millisecond)
	challenge, err := adapter.AuthorityChallenge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	allocation := policyauthority.Request{Binding: policyauthority.Binding{Identity: identity, NodeBoot: boot, ClientID: global.StableID, WindowID: policy.WindowID, PolicyVersion: 7}, RequestID: demand.Requests[0].RequestId, ChallengeID: challenge.ChallengeId, Capacity: 512, Upload: policy.Upload, Download: policy.Download, LeaseDuration: 10 * time.Second}
	issued, err := journal.Issue(allocation)
	if err != nil {
		t.Fatal(err)
	}
	grant := nodeControlHTTPGrant(issued)
	installed, err := adapter.InstallAuthorityGrant(ctx, grant)
	if err != nil || !proto.Equal(installed.Grant, grant) {
		t.Fatalf("actual canonical7 -> local1 grant install failed: %v", err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	sshHTTPEcho(t, flow, "tcp12345")
	udpTarget, err := net.ListenPacket("udp4", h.target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udpTarget.Close()
	go func() {
		buffer := make([]byte, 64)
		for {
			n, address, err := udpTarget.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = udpTarget.WriteTo(buffer[:n], address)
		}
	}()
	udp, err := net.DialTimeout("udp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	sshHTTPEcho(t, udp, "udp12345")
	used, err := adapter.GetAuthorityGrant(ctx, global.StableID, grant.GrantId)
	if err != nil || used == nil || used.Grant.ClientId != global.StableID || used.Grant.PolicyVersion != 7 || used.Usage.RawUpload != 16 || used.Usage.RawDownload != 16 || used.Usage.BilledBytes != 64 || used.Usage.Remainder != 0 {
		t.Fatalf("actual mapped TCP/UDP must bill raw16/16 once at2x=64: %+v/%v", used, err)
	}
	actualState, err := actual.GetClient(ctx, local.StableID)
	if err != nil || actualState.Policy.Version != 1 || actualState.Usage.BilledBytes != 64 || !actualState.AuthorityGrantHistory {
		t.Fatal("node identity/version/billing changed during translation", err)
	}
	report := policyauthority.Report{Binding: allocation.Binding, GrantID: grant.GrantId, Sequence: used.Sequence, Usage: policyauthority.Usage{RawUpload: used.Usage.RawUpload, RawDownload: used.Usage.RawDownload, BilledBytes: used.Usage.BilledBytes, Remainder: used.Usage.Remainder}}
	for i := 0; i < 2; i++ {
		if err := journal.Report(report); err != nil {
			t.Fatal(err)
		}
	}
	account, err := journal.Account(global.StableID)
	if err != nil || account.HeldCapacity != 448 || account.WindowUsed != 64 || account.Usage != (policyauthority.Usage{RawUpload: 16, RawDownload: 16, BilledBytes: 64}) {
		t.Fatal("canonical cumulative report doubled charge or released allowance", err)
	}
	if _, err := journal.LookupAccount(local.StableID); err != policyauthority.ErrNotFound {
		t.Fatal("coordinator charged a second local account", err)
	}
	sealed, err := adapter.SealAuthorityGrant(ctx, global.StableID, grant.GrantId)
	if err != nil || !sealed.Sealed || sealed.Usage.BilledBytes != 64 {
		t.Fatal("mapped seal failed", err)
	}
	report.Sequence, report.Seal = sealed.Sequence, true
	for i := 0; i < 2; i++ {
		if err := journal.Report(report); err != nil {
			t.Fatal(err)
		}
	}
	account, err = journal.Account(global.StableID)
	if err != nil || account.HeldCapacity != 0 || account.WindowUsed != 64 || account.Usage.BilledBytes != 64 {
		t.Fatal("mapped sealed conservation failed", err)
	}
	retry, err := coordinator.Enroll(ctx, pinned, request)
	if err != nil || retry.Mapping != proof.Mapping {
		t.Fatal("consumption retargeted original enrollment", err)
	}
	challenge, err = adapter.AuthorityChallenge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nextRequest := allocation
	nextRequest.RequestID = "mapped-restart-allocation"
	nextRequest.ChallengeID = challenge.ChallengeId
	nextRequest.Capacity = 256
	next, err := journal.Issue(nextRequest)
	if err != nil {
		t.Fatal(err)
	}
	nextGrant := nodeControlHTTPGrant(next)
	if _, err := adapter.InstallAuthorityGrant(ctx, nextGrant); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	fresh, err := peer.remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || fresh.Capabilities.InstanceId != boot.SourceID || fresh.Capabilities.BootId == boot.BootID || fresh.ExecutionRole == nil || *fresh.ExecutionRole != role.Role() {
		t.Fatal("restart lost original source/role", err)
	}
	if result, err := adapter.GetAuthorityGrant(ctx, global.StableID, nextGrant.GrantId); err == nil || result != nil {
		t.Fatal("stale mapped boot acknowledged original allocation")
	}
	account, err = journal.Account(global.StableID)
	if err != nil || account.HeldCapacity != 256 || account.Usage.BilledBytes != 64 {
		t.Fatal("restart released uncertain allocation or reset billing", err)
	}
	freshPinned, err := panelruntime.NewRemoteAuthorityAPI(ctx, peer.remote, panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: boot.SourceID, ExpectedBootID: fresh.Capabilities.BootId}, role.Role())
	if err != nil {
		t.Fatal(err)
	}
	freshRequest := request
	freshRequest.Binding.ExpectedBootID = fresh.Capabilities.BootId
	currentProof, err := coordinator.Enroll(ctx, freshPinned, freshRequest)
	if err != nil || currentProof.Mapping != proof.Mapping {
		t.Fatal("fresh boot replaced original mapping", err)
	}
	if err := h.svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	nodeJournal, err := policyauthority.Open(filepath.Join(filepath.Dir(manifestPath), "journal.db"), proof.Mapping.NodeAnchor)
	if err != nil {
		t.Fatal(err)
	}
	original, err := nodeJournal.LookupClientMapping(policyauthority.ClientMappingNode, boot.SourceID, local.StableID)
	if closeErr := nodeJournal.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || original != proof.Mapping {
		t.Fatal("original node journal reopen lost enrollment", err)
	}
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	changedBoot, err := peer.remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	newPinned, err := panelruntime.NewRemoteAuthorityAPI(ctx, peer.remote, panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: boot.SourceID, ExpectedBootID: changedBoot.Capabilities.BootId}, role.Role())
	if err != nil {
		t.Fatal(err)
	}
	newEndpoint, err := h.svc.GetXrayAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	newActual, err := xray.DialClientPolicy(ctx, newEndpoint, boot.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer newActual.Close()
	current, err := newActual.GetClient(ctx, local.StableID)
	if err != nil {
		t.Fatal(err)
	}
	changed := proto.Clone(current.Policy).(*clientpolicy.PolicyConfig)
	changed.Version++
	if err := newActual.Apply(ctx, []*clientpolicy.PolicyConfig{changed}); err != nil {
		t.Fatal(err)
	}
	changedRequest := request
	changedRequest.Binding.ExpectedBootID = changedBoot.Capabilities.BootId
	if result, err := coordinator.Enroll(ctx, newPinned, changedRequest); err == nil || result != nil {
		t.Fatal("actual policy version change reused immutable proof")
	}
	stored, err := journal.LookupClientMapping(policyauthority.ClientMappingCoordinator, boot.SourceID, local.StableID)
	if err != nil || stored != proof.Mapping {
		t.Fatal("policy refusal erased coordinator anchor", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := policyauthority.Open(journalPath, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err = reopened.LookupClientMapping(policyauthority.ClientMappingCoordinator, boot.SourceID, local.StableID)
	if err != nil || stored != proof.Mapping {
		t.Fatal("coordinator reopen lost original mapping", err)
	}
	account, err = reopened.Account(global.StableID)
	if err != nil || account.HeldCapacity != 256 || account.Usage.BilledBytes != 64 {
		t.Fatal("original coordinator reopening reset held/billed totals", err)
	}
}

func testNodeClientMappingLocalIssuer(t *testing.T) {
	h := newNativeHTTPHarness(t, "node_mapping_local_http")
	t.Logf("node client mapping local issuer HTTP backend: %s", database.GetDB().Dialector.Name())
	_, client := nodeControlHTTPClient(t, h)
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	peer := newNodeControlHTTPPeer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	found, err := peer.remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || found.ExecutionRole == nil || found.ExecutionRole.Mode != panelruntime.NodeExecutionLocal {
		t.Fatal("actual local issuer fixture unavailable", err)
	}
	endpoint, err := h.svc.GetXrayAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := xray.DialClientPolicy(ctx, endpoint, found.Capabilities.InstanceId)
	if err != nil {
		t.Fatal(err)
	}
	defer actual.Close()
	state, err := actual.GetClient(ctx, client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := panelruntime.EffectiveClientPolicyDigest(state.Policy)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority", "authority.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Identity policyauthority.Identity `json:"identity"`
	}
	if json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("invalid actual issuer manifest")
	}
	request := panelruntime.NodeClientMappingRequest{Binding: panelruntime.NodeAuthorityControlBinding{ExpectedInstanceID: found.Capabilities.InstanceId, ExpectedBootID: found.Capabilities.BootId, AuthorityID: manifest.Identity.AuthorityID, Generation: manifest.Identity.Generation, NodeID: "local"}, GlobalClientID: "11111111-1111-4111-8111-111111111111", LocalClientID: client.StableID, GlobalPolicyVersion: 7, LocalPolicyVersion: state.Policy.Version, ExpectedPolicyDigest: digest}
	if request.Validate() != nil {
		t.Fatal("local-issuer refusal request is not complete and valid")
	}
	if result, err := peer.remote.EnrollClientMapping(ctx, request); err == nil || result != nil {
		t.Fatal("actual local issuer acknowledged external mapping")
	}
}
