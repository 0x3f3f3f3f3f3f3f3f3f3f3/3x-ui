package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/xtls/xray-core/app/clientpolicy"
)

func TestClientPolicyNodeAccountsIndependentAndRetained(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	t.Logf("node policy account backend: %s", db.Dialector.Name())
	parent := model.ClientRecord{Email: "node-account-parent", Enable: true, TotalGB: 128, Policy: &model.ClientPolicyOptions{Multiplier: "1.5"}}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	prepare := func(node, source string) (model.ClientPolicyNodeAccount, clientpolicy.Policy, error) {
		return prepareNodeClientPolicyContextForDatabase(context.Background(), db, parent.StableID, node, source)
	}
	a, ap, err := prepare("actual-node-a", "original-source-a")
	if err != nil {
		t.Fatal(err)
	}
	b, bp, err := prepare("actual-node-b", "original-source-b")
	if err != nil || a.ClientID == b.ClientID || a.ClientID == parent.StableID || b.ClientID == parent.StableID || ap.ClientID != a.ClientID || bp.ClientID != b.ClientID || ap.Version != 1 || bp.Version != 1 || ap.QuotaBytes != 128 || bp.QuotaBytes != 128 {
		t.Fatalf("independent accounts unavailable: %+v/%+v/%v", a, b, err)
	}
	retry, retryPolicy, err := prepare(a.NodeID, a.SourceID)
	if err != nil || retry != a || retryPolicy != ap {
		t.Fatalf("retry recreated allowance identity: %+v/%v", retry, err)
	}
	if _, _, err := prepare(a.NodeID, "replacement-source"); err == nil {
		t.Fatal("source replacement created fresh account on the same node")
	}
	if _, _, err := prepare("replacement-node", a.SourceID); err == nil {
		t.Fatal("same original source acquired a second node account")
	}
	var clientCount int64
	if err := db.Model(&model.ClientRecord{}).Count(&clientCount).Error; err != nil || clientCount != 1 {
		t.Fatal("node accounts appeared as ordinary duplicate clients")
	}
	if err := db.Model(&parent).Update("policy_multiplier", "2").Error; err != nil {
		t.Fatal(err)
	}
	updated, updatedPolicy, err := prepare(a.NodeID, a.SourceID)
	if err != nil || updated.ClientID != a.ClientID || updatedPolicy.Version != 2 || updatedPolicy.Multiplier != 2000000 {
		t.Fatalf("node account version did not advance monotonically: %+v/%v", updatedPolicy, err)
	}
	request := panelruntime.NodeClientMappingRequest{LocalClientID: a.ClientID, LocalPolicyVersion: 2, Binding: panelruntime.NodeAuthorityControlBinding{NodeID: a.NodeID, ExpectedInstanceID: a.SourceID}}
	desired, err := mappingDesiredPolicy(db, request)
	if err != nil || desired.ClientId != a.ClientID || desired.Version != 2 || desired.MultiplierMicros != 2000000 {
		t.Fatalf("canonical node account cannot resolve its full policy: %+v/%v", desired, err)
	}
	request.Binding.ExpectedInstanceID = "another-original-source"
	if _, err := mappingDesiredPolicy(db, request); err == nil {
		t.Fatal("canonical node account authorized another source")
	}
	var unchanged model.ClientPolicyNodeAccount
	if err := db.First(&unchanged, "client_id = ?", b.ClientID).Error; err != nil || unchanged != b {
		t.Fatal("preparing node A rewrote independent node B")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := prepareNodeClientPolicyContextForDatabase(canceled, db, parent.StableID, "cancel-node", "cancel-source"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation entered account mutation: %v", err)
	}
	if _, _, err := prepareNodeClientPolicyContextForDatabase(nil, db, parent.StableID, a.NodeID, a.SourceID); err == nil {
		t.Fatal("nil context admitted account preparation")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			retry, p, err := prepare(a.NodeID, a.SourceID)
			if err != nil || retry != updated || p != updatedPolicy {
				t.Errorf("concurrent preparation changed retained identity/version: %+v/%v", retry, err)
			}
		})
	}
	wg.Wait()
	for _, duplicate := range []model.ClientPolicyNodeAccount{
		{ParentClientID: parent.StableID, NodeID: a.NodeID, SourceID: "another-source"},
		{ParentClientID: parent.StableID, NodeID: "another-node", SourceID: a.SourceID},
	} {
		if err := db.Create(&duplicate).Error; err == nil {
			t.Fatal("database admitted a duplicate original node/source account")
		}
	}
	large := updated
	large.DesiredPolicyVersion = 9007199254740993
	raw, err := json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Version string `json:"desiredPolicyVersion"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Version != "9007199254740993" {
		t.Fatalf("account version lost decimal-string precision: %s/%v", raw, err)
	}
	if err := db.Model(&parent).Update("policy_multiplier", "0").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepare("rollback-node", "rollback-source"); !errors.Is(err, clientpolicy.ErrInvalidPolicy) {
		t.Fatalf("invalid parent policy admitted account: %v", err)
	}
	var accountCount int64
	if err := db.Model(&model.ClientPolicyNodeAccount{}).Count(&accountCount).Error; err != nil || accountCount != 2 {
		t.Fatalf("failed policy preparation left a partial account: %d/%v", accountCount, err)
	}
	if err := db.Model(&parent).Update("policy_multiplier", "2").Error; err != nil {
		t.Fatal(err)
	}
	reset := model.ClientPolicyReset{ClientID: a.ClientID, RequestID: "node-a-reset", InstanceID: a.SourceID, Epoch: 1, Sequence: 1, BilledBytes: 10, UncertainBytes: 1, Remainder: 500000, PolicyVersion: 2}
	if err := db.Create(&reset).Error; err != nil {
		t.Fatal(err)
	}
	resetAccount, resetPolicy, err := prepare(a.NodeID, a.SourceID)
	if err != nil || resetAccount.ClientID != a.ClientID || resetPolicy.Version != 3 || resetPolicy.QuotaBaselineBytes != 11 || resetPolicy.QuotaBaselineRemainder != 500000 {
		t.Fatalf("account reset boundary lost exact billed/fraction history: %+v/%v", resetPolicy, err)
	}
	var independent model.ClientPolicyNodeAccount
	if err := db.First(&independent, "client_id = ?", b.ClientID).Error; err != nil || independent != b {
		t.Fatal("node A reset rewrote node B account")
	}
	if err := db.Create(&model.ClientPolicyTombstone{ClientID: a.ClientID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepare(a.NodeID, a.SourceID); !errors.Is(err, clientpolicy.ErrRevoked) {
		t.Fatalf("deleted canonical account regained policy: %v", err)
	}
	if err := db.Create(&model.ClientPolicyTombstone{ClientID: parent.StableID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepare(a.NodeID, a.SourceID); !errors.Is(err, clientpolicy.ErrRevoked) {
		t.Fatalf("deleted parent regained an account: %v", err)
	}
	var retained model.ClientPolicyNodeAccount
	if err := db.First(&retained, "client_id = ?", a.ClientID).Error; err != nil || retained.ClientID != a.ClientID {
		t.Fatal("deletion removed retained account identity")
	}
}
