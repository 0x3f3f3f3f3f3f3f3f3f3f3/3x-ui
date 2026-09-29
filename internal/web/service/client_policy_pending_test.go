package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func accountingPolicyPending(t *testing.T, accounting *xray.ClientPolicyAccounting) bool {
	t.Helper()
	if accounting == nil {
		t.Fatal("missing confirmed accounting")
	}
	raw, err := json.Marshal(accounting)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		PolicyPending bool `json:"policyPending"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	return status.PolicyPending
}

func TestClientPolicyAccountingPendingIncludesSavedUnpreparedChanges(t *testing.T) {
	for _, change := range []struct {
		column string
		value  any
	}{
		{"policy_upload_bytes_per_second", int64(4096)},
		{"policy_download_bytes_per_second", int64(8192)},
		{"policy_multiplier", "2.5"},
		{"total_gb", int64(1024)},
		{"enable", false},
		{"expiry_time", int64(2000000000000)},
	} {
		t.Run(change.column, func(t *testing.T) {
			id := resetLedgerFixture(t)
			read := func(applied, desired string, pending bool) {
				t.Helper()
				traffic, err := (&InboundService{}).GetClientTrafficByEmail("reset-fixture")
				if err != nil {
					t.Fatal(err)
				}
				got := traffic.Accounting
				if accountingPolicyPending(t, got) != pending || got.AppliedVersion != applied || got.DesiredVersion != desired || got.Lifetime.Billed != "1.5" {
					t.Fatalf("saved policy pending=%v, versions=%s/%s: %+v", pending, applied, desired, got)
				}
			}
			read("1", "1", false)
			if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", id).Update(change.column, change.value).Error; err != nil {
				t.Fatal(err)
			}
			read("1", "1", true)
			if _, err := PrepareClientPolicies([]string{id}); err != nil {
				t.Fatal(err)
			}
			read("1", "2", true)
			page := policyLedgerPage(id, 2, 3, 0, 1)
			page.Records[0].PolicyVersion = 2
			page.Records[0].Usage.Remainder = 500000
			page.Records[0].UncertainBytes = 7
			if err := SettleClientPolicyLedger("core-a", 1, 1, page); err != nil {
				t.Fatal(err)
			}
			read("2", "2", false)
			if _, err := PrepareClientPolicyReset("core-a", id, "pending-status-reset"); err != nil {
				t.Fatal(err)
			}
			read("2", "3", true)
			page.Records[0].Sequence, page.NextSequence = 3, 3
			page.Records[0].PolicyVersion = 3
			if err := SettleClientPolicyLedger("core-a", 1, 2, page); err != nil {
				t.Fatal(err)
			}
			read("3", "3", false)
		})
	}
}
