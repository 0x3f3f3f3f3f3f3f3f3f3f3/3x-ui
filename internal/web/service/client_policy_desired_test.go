package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestClientPolicyDesiredVersionsPreserveBillingAndDisable(t *testing.T) {
	setupPolicyLedgerDB(t)
	record := model.ClientRecord{Email: "desired-policy", Enable: true, TotalGB: 1000}
	if err := database.GetDB().Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	prepare := func() clientpolicy.Policy {
		t.Helper()
		policies, err := PrepareClientPolicies([]string{record.StableID})
		if err != nil || len(policies) != 1 {
			t.Fatalf("prepare: %v, %v", policies, err)
		}
		return policies[0]
	}
	first := prepare()
	if first.Version != 1 || first.Multiplier != 1000000 || first.QuotaBytes != 1000 || first.UploadRate != 0 || first.DownloadRate != 0 {
		t.Fatalf("legacy defaults changed: %+v", first)
	}
	if err := database.GetDB().Save(&record).Error; err != nil {
		t.Fatal(err)
	}
	var stored model.ClientRecord
	if err := database.GetDB().First(&stored, record.Id).Error; err != nil || stored.DesiredPolicyVersion != 1 {
		t.Fatalf("ordinary account save rewound the policy version: %+v, %v", stored, err)
	}
	engine := clientpolicy.NewEngine()
	defer engine.Close()
	if err := engine.Apply(first); err != nil {
		t.Fatal(err)
	}
	var closed atomic.Bool
	session, err := engine.Open(context.Background(), clientpolicy.Metadata{ClientID: record.StableID}, func() { closed.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Admit(clientpolicy.Upload, 10); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&record).Updates(map[string]any{"email": "renamed-desired", "uuid": "rotated", "comment": "metadata"}).Error; err != nil {
		t.Fatal(err)
	}
	if got := prepare(); got != first {
		t.Fatalf("metadata edit changed policy: %+v", got)
	}
	if err := database.GetDB().Model(&record).Updates(map[string]any{"policy_multiplier": "2", "policy_download_bytes_per_second": 1048576}).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			policies, err := PrepareClientPolicies([]string{record.StableID})
			if err != nil || len(policies) != 1 || policies[0].Version != 2 {
				t.Errorf("concurrent version: %+v, %v", policies, err)
			}
		})
	}
	wg.Wait()
	second := prepare()
	if second.Version != 2 || second.Multiplier != 2000000 || second.DownloadRate != 1048576 {
		t.Fatalf("policy update missing: %+v", second)
	}
	if err := engine.Apply(second); err != nil {
		t.Fatal(err)
	}
	if err := session.Admit(clientpolicy.Download, 20); err != nil {
		t.Fatal(err)
	}
	usage, err := engine.Snapshot(record.StableID)
	if err != nil || usage.Usage.RawUpload != 10 || usage.Usage.RawDownload != 20 || usage.Usage.BilledBytes != 50 {
		t.Fatalf("history was repriced: %+v, %v", usage, err)
	}
	if err := database.GetDB().Model(&record).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	disabled := prepare()
	if disabled.Version != 3 || disabled.Enabled {
		t.Fatalf("disable absent: %+v", disabled)
	}
	if err := engine.Apply(disabled); err != nil {
		t.Fatal(err)
	}
	if !closed.Load() {
		t.Fatal("existing session survived disable")
	}
	if err := database.GetDB().Model(&record).Update("total_gb", 2000).Error; err != nil {
		t.Fatal(err)
	}
	renewed := prepare()
	if renewed.Version != 4 || renewed.Enabled || renewed.QuotaBytes != 2000 {
		t.Fatalf("quota edit overrode manual disable: %+v", renewed)
	}
	if err := engine.Apply(renewed); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Open(context.Background(), clientpolicy.Metadata{ClientID: record.StableID}, nil); !errors.Is(err, clientpolicy.ErrRestricted) {
		t.Fatalf("disabled client reopened: %v", err)
	}
}

func TestClientPolicyDesiredRejectsInvalidBatchAtomically(t *testing.T) {
	setupPolicyLedgerDB(t)
	a := model.ClientRecord{Email: "first", Enable: true}
	b := model.ClientRecord{Email: "second", Enable: true}
	if err := database.GetDB().Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicies([]string{a.StableID, b.StableID}); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&a).Update("total_gb", 999).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&b).Update("policy_multiplier", "0").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicies([]string{a.StableID, b.StableID}); !errors.Is(err, clientpolicy.ErrInvalidPolicy) {
		t.Fatalf("invalid batch accepted: %v", err)
	}
	var versions []int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Order("id").Pluck("desired_policy_version", &versions).Error; err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 1 {
		t.Fatalf("rejected batch committed versions: %v", versions)
	}
}
