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
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
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
	if err := f.journal.Tombstone(r.GlobalClientID); err != nil {
		t.Fatal(err)
	}
	if result, err := svc.AuthorityAPI(context.Background(), pinned); err == nil || result != nil {
		t.Fatal("deleted global account retained execution API")
	}
}
