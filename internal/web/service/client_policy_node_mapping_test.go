package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestCoordinatorClientMappingPinsOriginalJournal(t *testing.T) {
	f := newNodeControlFixture(t)
	t.Logf("coordinator client mapping backend: %s", database.GetDB().Dialector.Name())
	r := nodeMappingRequest(t, f)
	global := model.ClientRecord{StableID: r.GlobalClientID, Email: "canonical-mapping-owner", Enable: true, TotalGB: f.client.TotalGB, Policy: &model.ClientPolicyOptions{Multiplier: "2"}, DesiredPolicyVersion: 7}
	_, fingerprint, err := fingerprintClientPolicy(global, nil)
	if err != nil {
		t.Fatal(err)
	}
	global.PolicyFingerprint = fingerprint
	if err := database.GetDB().Create(&global).Error; err != nil {
		t.Fatal(err)
	}
	account, err := f.owner.state.Journal.Account(r.LocalClientID)
	if err != nil {
		t.Fatal(err)
	}
	seed := account.Seed
	seed.ClientID = r.GlobalClientID
	seed.Policy.Version = 7
	seed.Policy.WindowID = "canonical-window-7"
	if err := f.journal.AddAccount(seed); err != nil {
		t.Fatal(err)
	}
	before, err := f.journal.Account(r.GlobalClientID)
	if err != nil {
		t.Fatal(err)
	}
	var changeDuringProof atomic.Bool
	var peerCalls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		peerCalls.Add(1)
		var result any
		var err error
		if req.URL.Path == "/panel/api/server/clientPolicyAuthority" {
			var body panelruntime.AuthorityDiscoveryRequest
			err = json.NewDecoder(req.Body).Decode(&body)
			if err == nil {
				result, err = f.node.DiscoverAuthority(req.Context(), body)
			}
		} else if req.URL.Path == "/panel/api/server/clientPolicyAuthority/enroll" {
			var body panelruntime.NodeClientMappingRequest
			err = json.NewDecoder(req.Body).Decode(&body)
			if err == nil {
				result, err = f.node.EnrollClientMapping(req.Context(), body)
			}
			if err == nil && changeDuringProof.CompareAndSwap(true, false) {
				err = database.GetDB().Table("clients").Where("stable_id = ?", r.GlobalClientID).Update("total_gb", 9999).Error
			}
		} else if req.URL.Path == "/panel/api/server/clientPolicyAuthority/get" {
			var body panelruntime.NodeAuthorityGrantRequest
			err = json.NewDecoder(req.Body).Decode(&body)
			if err == nil {
				result, err = f.node.GetAuthorityGrant(req.Context(), body)
			}
		} else {
			w.WriteHeader(404)
			return
		}
		if err != nil {
			t.Logf("actual node operation %s failed: %v", req.URL.Path, err)
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
	remote := panelruntime.NewRemote(&model.Node{Id: 1, Name: "mapping-node", Scheme: "https", Address: address.Hostname(), Port: port, BasePath: "/", Enable: true, AllowPrivateAddress: true, TlsVerifyMode: "pin", PinnedCertSha256: base64.StdEncoding.EncodeToString(cert[:]), ApiToken: "mapping-test-token"}, nil)
	pinned, err := panelruntime.NewRemoteAuthorityAPI(context.Background(), remote, panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: r.Binding.ExpectedInstanceID, ExpectedBootID: r.Binding.ExpectedBootID}, r.Binding.Role())
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewClientPolicyNodeMappingService(database.GetDB(), f.journal)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := svc.Enroll(context.Background(), pinned, r)
	if err != nil || proof.Validate(r) != nil || proof.Mapping.NodeAnchor != f.owner.state.Journal.Identity() {
		t.Fatalf("actual node proof not retained by original coordinator: %+v/%v", proof, err)
	}
	stored, err := f.journal.LookupClientMapping(policyauthority.ClientMappingCoordinator, r.Binding.ExpectedInstanceID, r.LocalClientID)
	if err != nil || stored != proof.Mapping {
		t.Fatal("coordinator lacks original node evidence")
	}
	if err := database.GetDB().Where("source_id = ? AND local_client_id = ?", r.Binding.ExpectedInstanceID, r.LocalClientID).Delete(&model.ClientPolicyNodeMapping{}).Error; err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	fail.Store(true)
	const callback = "mapping-test-projection-failure"
	if err := database.GetDB().Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if fail.Load() && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "client_policy_node_mappings" {
			tx.AddError(errors.New("injected projection write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.GetDB().Callback().Create().Remove(callback) })
	if result, err := svc.Enroll(context.Background(), pinned, r); err == nil || result != nil {
		t.Fatal("failed SQL projection acknowledged enrollment")
	}
	retained, err := f.journal.LookupClientMapping(policyauthority.ClientMappingCoordinator, r.Binding.ExpectedInstanceID, r.LocalClientID)
	if err != nil || retained != stored {
		t.Fatal("SQL rollback erased original journal mapping")
	}
	fail.Store(false)
	retry, err := svc.Enroll(context.Background(), pinned, r)
	if err != nil || retry == nil || retry.Mapping != stored {
		t.Fatal("lost projection failed exact recovery", err)
	}
	var rows int64
	if err := database.GetDB().Model(&model.ClientPolicyNodeMapping{}).Count(&rows).Error; err != nil || rows != 1 {
		t.Fatal("projection recovery duplicated enrollment", rows, err)
	}
	changeDuringProof.Store(true)
	if result, err := svc.Enroll(context.Background(), pinned, r); err == nil || result != nil {
		t.Fatal("canonical policy changed during peer proof but was committed")
	}
	retained, err = f.journal.LookupClientMapping(policyauthority.ClientMappingCoordinator, r.Binding.ExpectedInstanceID, r.LocalClientID)
	if err != nil || retained != stored {
		t.Fatal("rejected concurrent policy change altered original evidence")
	}
	if err := database.GetDB().Table("clients").Where("stable_id = ?", r.GlobalClientID).Update("total_gb", 10000).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&model.ClientPolicyNodeMapping{}).Where("source_id = ?", r.Binding.ExpectedInstanceID).Update("mapping_json", `{"untrusted":true}`).Error; err != nil {
		t.Fatal(err)
	}
	api, err := svc.AuthorityAPI(context.Background(), pinned)
	if err != nil || api == nil || api.Capabilities().InstanceId != r.Binding.ExpectedInstanceID {
		t.Fatal("factory trusted SQL instead of original journal", err)
	}
	after, err := f.journal.Account(r.GlobalClientID)
	if err != nil || before != after {
		t.Fatal("mapping changed global balance or policy")
	}
	bad := r
	bad.GlobalClientID = "33333333-3333-4333-8333-333333333333"
	if result, err := svc.Enroll(context.Background(), pinned, bad); err == nil || result != nil {
		t.Fatal("unknown global identity admitted")
	}
	localB := *f.client
	localB.Id, localB.StableID, localB.Email = 0, "44444444-4444-4444-8444-444444444444", "mapping-local-b"
	localB.DesiredPolicyVersion = 1
	_, localFingerprint, err := fingerprintClientPolicy(localB, nil)
	if err != nil {
		t.Fatal(err)
	}
	localB.PolicyFingerprint = localFingerprint
	if err := database.GetDB().Create(&localB).Error; err != nil {
		t.Fatal(err)
	}
	actualA, err := f.owner.api.GetClient(context.Background(), r.LocalClientID)
	if err != nil {
		t.Fatal(err)
	}
	policyB := proto.Clone(actualA.Policy).(*clientpolicy.PolicyConfig)
	policyB.ClientId = localB.StableID
	if err := f.owner.api.Apply(context.Background(), []*clientpolicy.PolicyConfig{policyB}); err != nil {
		t.Fatal(err)
	}
	localSeed := account.Seed
	localSeed.ClientID = localB.StableID
	if err := f.owner.state.Journal.AddAccount(localSeed); err != nil {
		t.Fatal(err)
	}
	globalB := global
	globalB.Id, globalB.StableID, globalB.Email = 0, "55555555-5555-4555-8555-555555555555", "mapping-global-b"
	_, globalFingerprint, err := fingerprintClientPolicy(globalB, nil)
	if err != nil {
		t.Fatal(err)
	}
	globalB.PolicyFingerprint = globalFingerprint
	if err := database.GetDB().Create(&globalB).Error; err != nil {
		t.Fatal(err)
	}
	globalSeed := seed
	globalSeed.ClientID = globalB.StableID
	if err := f.journal.AddAccount(globalSeed); err != nil {
		t.Fatal(err)
	}
	requestB := r
	requestB.LocalClientID, requestB.GlobalClientID = localB.StableID, globalB.StableID
	proofB, err := svc.Enroll(context.Background(), pinned, requestB)
	if err != nil || proofB == nil {
		t.Fatal("second actual node policy enrollment failed", err)
	}
	assertValidB := func() {
		t.Helper()
		if result, err := svc.AuthorityAPI(context.Background(), pinned); err != nil || result == nil {
			t.Fatal("inactive A blocked independently proven B", err)
		}
		storedB, err := f.journal.LookupClientMapping(policyauthority.ClientMappingCoordinator, requestB.Binding.ExpectedInstanceID, requestB.LocalClientID)
		if err != nil || storedB != proofB.Mapping {
			t.Fatal("filtering erased or retargeted B evidence", err)
		}
	}
	assertValidB()
	if err := database.GetDB().Table("clients").Where("stable_id = ?", r.GlobalClientID).Update("total_gb", 9999).Error; err != nil {
		t.Fatal(err)
	}
	assertValidB()
	if err := database.GetDB().Table("clients").Where("stable_id = ?", r.GlobalClientID).Update("total_gb", 10000).Error; err != nil {
		t.Fatal(err)
	}
	advanced := seed.Policy
	advanced.Version++
	if _, err := f.journal.ChangePolicy(policyauthority.ChangeRequest{Identity: f.journal.Identity(), ClientID: r.GlobalClientID, RequestID: "mapping-a-version-ahead", ExpectedVersion: seed.Policy.Version, Policy: advanced}); err != nil {
		t.Fatal(err)
	}
	assertValidB()
	if result, err := svc.Enroll(context.Background(), pinned, r); err == nil || result != nil {
		t.Fatal("obsolete A regained enrollment authority")
	}
	if err := f.journal.Tombstone(r.GlobalClientID); err != nil {
		t.Fatal(err)
	}
	assertValidB()
	filtered, err := svc.AuthorityAPI(context.Background(), pinned)
	if err != nil {
		t.Fatal(err)
	}
	beforePeer := peerCalls.Load()
	if result, err := filtered.GetAuthorityGrant(context.Background(), r.GlobalClientID, "unknown-grant"); err == nil || result != nil || peerCalls.Load() != beforePeer {
		t.Fatal("deleted A retained grant access in current factory")
	}
	if result, err := filtered.GetAuthorityGrant(context.Background(), requestB.GlobalClientID, "unknown-grant"); err == nil || result != nil || peerCalls.Load() != beforePeer+1 {
		t.Fatal("valid B could not reach its actual node grant operation", err)
	}
	retained, err = f.journal.LookupClientMapping(policyauthority.ClientMappingCoordinator, r.Binding.ExpectedInstanceID, r.LocalClientID)
	if err != nil || retained != stored {
		t.Fatal("filtering erased original inactive A evidence", err)
	}
	if err := database.GetDB().Migrator().DropTable(&model.ClientPolicyTombstone{}); err != nil {
		t.Fatal(err)
	}
	if result, err := svc.AuthorityAPI(context.Background(), pinned); err == nil || result != nil {
		t.Fatal("database failure was silently filtered")
	}
	if err := database.GetDB().AutoMigrate(&model.ClientPolicyTombstone{}); err != nil {
		t.Fatal(err)
	}
}
