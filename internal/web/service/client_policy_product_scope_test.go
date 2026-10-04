package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestClientPolicyProductScopeLegacyCompatibility(t *testing.T) {
	setupPolicyLedgerDB(t)
	record := model.ClientRecord{Email: "scope-compatibility", Enable: true, TotalGB: 128, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := database.GetDB().Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	policy, legacy, err := fingerprintClientPolicy(record, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if legacy != hex.EncodeToString(hash[:]) {
		t.Fatal("legacy node fingerprint bytes changed")
	}
	node, global := model.ClientPolicyScopeNode, model.ClientPolicyScopeGlobal
	record.Policy.Scope = &node
	_, explicitNode, err := fingerprintClientPolicy(record, nil)
	if err != nil || explicitNode != legacy {
		t.Fatal("explicit node silently advances legacy policy")
	}
	record.Policy.Scope = &global
	_, globalFingerprint, err := fingerprintClientPolicy(record, nil)
	if err != nil || globalFingerprint == legacy {
		t.Fatal("global scope did not advance policy identity")
	}
	clone := record.Policy.Clone()
	if !sameClientPolicy(record.Policy, clone) {
		t.Error("separate equal scope pointers rejected as a policy change")
	}
	applyClientRecordMerge(&record, &model.ClientRecord{Policy: &model.ClientPolicyOptions{Multiplier: "1.5"}})
	if record.Policy.EffectiveScope() != global || record.Policy.Multiplier != "1.5" {
		t.Error("legacy nested edit erased explicit global scope")
	}
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("id = ?", record.Id).Update("policy_scope", string(global)).Error; err != nil {
		t.Fatal(err)
	}
	var update model.Client
	if err := json.Unmarshal([]byte(`{"email":"scope-compatibility","enable":true,"policy":{"uploadBytesPerSecond":0,"downloadBytesPerSecond":0,"multiplier":"3"}}`), &update); err != nil {
		t.Fatal(err)
	}
	svc := ClientService{}
	if _, err := svc.Update(&InboundService{}, record.Id, update, 0); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.GetByID(record.Id)
	if err != nil || stored.Policy.EffectiveScope() != global || stored.Policy.Multiplier != "3" {
		t.Fatalf("persisted legacy edit lost global scope: %+v/%v", stored, err)
	}
}
