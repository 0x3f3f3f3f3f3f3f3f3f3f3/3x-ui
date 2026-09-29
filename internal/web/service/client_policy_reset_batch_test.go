package service

import (
	"errors"
	"slices"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func resetBatchFixture(t *testing.T) []string {
	t.Helper()
	setupPolicyLedgerDB(t)
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	ids := []string{policyLedgerClient(t, "batch-a", 0, 0), policyLedgerClient(t, "batch-b", 0, 0)}
	slices.Sort(ids)
	if _, err := PrepareClientPolicies(ids); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		page := policyLedgerPage(id, uint64(i+1), 3, 0, 1)
		page.Records[0].Usage.Remainder = 500000
		page.Records[0].UncertainBytes, page.Records[0].ReservedBytes = 7, 10
		if err := SettleClientPolicyLedger("core-a", 1, uint64(i), page); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func TestClientPolicyResetBatchRollsBackEveryBoundary(t *testing.T) {
	ids := resetBatchFixture(t)
	db := database.GetDB()
	if err := db.Model(&model.ClientPolicyTotal{}).Where("client_id = ?", ids[1]).Update("billed_bytes", 2).Error; err != nil {
		t.Fatal(err)
	}
	policies, err := PrepareClientPolicyResets("core-a", ids, "batch-failure")
	if !errors.Is(err, ErrClientPolicyLedger) || len(policies) != 0 {
		t.Fatalf("corrupt member produced an applicable batch: %+v, %v", policies, err)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed batch left reset boundaries: %d, %v", count, err)
	}
	var clients []model.ClientRecord
	if err := db.Find(&clients).Error; err != nil {
		t.Fatal(err)
	}
	for _, client := range clients {
		if client.DesiredPolicyVersion != 1 {
			t.Fatalf("failed batch advanced %s to version %d", client.StableID, client.DesiredPolicyVersion)
		}
	}
}

func TestClientPolicyResetBatchRetriesKeepIndependentLatestWindows(t *testing.T) {
	ids := resetBatchFixture(t)
	db := database.GetDB()
	if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", ids[1]).Updates(map[string]any{"enable": false, "expiry_time": 12345}).Error; err != nil {
		t.Fatal(err)
	}
	first, err := PrepareClientPolicyResets("core-a", ids, "batch-one")
	if err != nil || len(first) != 2 {
		t.Fatalf("prepare batch: %+v, %v", first, err)
	}
	byID := make(map[string]clientpolicy.Policy)
	for _, policy := range first {
		if policy.Version != 2 || policy.QuotaBaselineBytes != 8 || policy.QuotaBaselineRemainder != 500000 {
			t.Fatalf("batch credited reservations or lost exact boundary: %+v", policy)
		}
		byID[policy.ClientID] = policy
	}
	if byID[ids[1]].Enabled || byID[ids[1]].ExpiresAt != 12345 {
		t.Fatalf("batch removed independent restrictions: %+v", byID[ids[1]])
	}
	page := policyLedgerPage(ids[0], 3, 7, 0, 3)
	page.Records[0].PolicyVersion = 2
	page.Records[0].Usage.Remainder = 500000
	page.Records[0].UncertainBytes = 7
	if err := SettleClientPolicyLedger("core-a", 1, 2, page); err != nil {
		t.Fatal(err)
	}
	latest, err := PrepareClientPolicyReset("core-a", ids[0], "later-single")
	if err != nil || latest.Version != 3 || latest.QuotaBaselineBytes != 10 {
		t.Fatalf("later single reset: %+v, %v", latest, err)
	}
	byID[ids[0]] = latest
	retry, err := PrepareClientPolicyResets("core-a", []string{ids[1], ids[0]}, "batch-one")
	if err != nil || len(retry) != 2 {
		t.Fatalf("retry batch: %+v, %v", retry, err)
	}
	for _, policy := range retry {
		if policy != byID[policy.ClientID] {
			t.Fatalf("late batch retry replaced a newer window: %+v, want %+v", policy, byID[policy.ClientID])
		}
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("retry created additional quota windows: %d, %v", count, err)
	}
	if total := policyLedgerTotal(t, ids[0]); total.RawUpload != 7 || total.BilledBytes != 3 || total.UncertainBytes != 7 {
		t.Fatalf("batch changed lifetime usage: %+v", total)
	}
}
