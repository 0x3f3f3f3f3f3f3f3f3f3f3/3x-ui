package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPortablePolicy_Postgres(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"segmented", TestPortablePolicyPreservesSegmentedCharges},
		{"exact-bytes", TestPortableBytesSurviveBrowserJSON},
		{"large-attached", TestPortablePolicyPreservesLargeAttachedQuotaAndCharges},
		{"wrong-endpoint", TestPortablePolicyCannotBeIgnoredByCreateEndpoints},
		{"invalid", TestPortablePolicyRejectsInvalidSnapshotsAtomically},
		{"snapshot", TestPortableExportUsesOneAccountingSnapshot},
		{"retained", TestPortablePolicyReplacesRetainedTrafficWithoutReusingMeters},
		{"restrictions", TestPortablePolicyPreservesIndependentRestrictions},
	} {
		t.Run(test.name, func(t *testing.T) {
			if os.Getenv("XUI_TEST_PG_DSN") == "" {
				t.Skip("set XUI_TEST_PG_DSN to an isolated PostgreSQL instance")
			}
			t.Setenv("XUI_DB_TYPE", "postgres")
			test.run(t)
		})
	}
}

func TestPortableExportUsesOneAccountingSnapshot(t *testing.T) {
	portableTestSchema(t)
	record := clientPolicyFixture(t)
	db, svc := database.GetDB(), &ClientService{}
	ledger := database.NewClientUsageLedger(db)
	meter, err := ledger.ClaimAdmissionSource(context.Background(), record.PolicyID, "portable/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	selected, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	t.Cleanup(resume)
	var intercepted atomic.Bool
	const callback = "test:portable_export_snapshot"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Error == nil && tx.Statement.Table == "clients" && intercepted.CompareAndSwap(false, true) {
			close(selected)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	type exportResult struct {
		items []ClientCreatePayload
		err   error
	}
	done := make(chan exportResult, 1)
	go func() {
		items, err := svc.ExportAll()
		done <- exportResult{items: items, err: err}
	}()
	select {
	case <-selected:
	case result := <-done:
		t.Fatalf("export never reached client snapshot: %v", result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("export did not reach snapshot barrier")
	}
	if _, _, err := svc.BulkAdjust(&InboundService{}, []string{record.Email}, 0, 1000, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(context.Background(), database.ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 1}); err != nil {
		t.Fatal(err)
	}
	resume()
	select {
	case result := <-done:
		if result.err != nil || len(result.items) != 1 {
			t.Fatalf("export=%+v err=%v", result.items, result.err)
		}
		item := result.items[0]
		if item.Client.TotalGB != 1000 || item.Traffic == nil || item.Traffic.Up != 0 || item.Policy == nil || item.Policy.Billed != "0" || item.Policy.TrafficTotal != "1000" {
			t.Fatalf("export mixed states across a concurrent write: client=%+v traffic=%+v policy=%+v", item.Client, item.Traffic, item.Policy)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("export did not finish after releasing barrier")
	}
	current, err := svc.GetPolicy(context.Background(), record.Email)
	if err != nil || current.Usage.Up != "1" || current.Usage.Quota != "2000" {
		t.Fatalf("concurrent change was not exercised: %+v err=%v", current, err)
	}
}

func TestPortablePolicyRejectsInvalidSnapshotsAtomically(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ClientCreatePayload)
		reason string
	}{
		{"future-format", func(p *ClientCreatePayload) { p.Policy.FormatVersion = 2 }, "formatVersion"},
		{"scope", func(p *ClientCreatePayload) { p.Policy.Scope = "global" }, ErrClientPolicyUnsupported.Error()},
		{"negative-rate", func(p *ClientCreatePayload) { p.Policy.UploadBps = -1 }, clientpolicy.ErrInvalidRate.Error()},
		{"large-rate", func(p *ClientCreatePayload) { p.Policy.DownloadBps = clientpolicy.MaxRate + 1 }, clientpolicy.ErrInvalidRate.Error()},
		{"zero-multiplier", func(p *ClientCreatePayload) { p.Policy.Multiplier = "0" }, clientpolicy.ErrInvalidMultiplier.Error()},
		{"precision", func(p *ClientCreatePayload) { p.Policy.Multiplier = "1.0001" }, clientpolicy.ErrInvalidMultiplier.Error()},
		{"negative-billed", func(p *ClientCreatePayload) { p.Policy.Billed = "-1" }, clientpolicy.ErrInvalidUsage.Error()},
		{"overflow-billed", func(p *ClientCreatePayload) { p.Policy.Billed = "9223372036854775808" }, clientpolicy.ErrInvalidUsage.Error()},
		{"fraction-billed", func(p *ClientCreatePayload) { p.Policy.Billed = "1.5" }, clientpolicy.ErrInvalidUsage.Error()},
		{"carry", func(p *ClientCreatePayload) { p.Policy.Remainder = 1000 }, clientpolicy.ErrInvalidUsage.Error()},
		{"traffic-quota", func(p *ClientCreatePayload) { p.Policy.TrafficTotal = "-1" }, clientpolicy.ErrInvalidUsage.Error()},
		{"missing-traffic", func(p *ClientCreatePayload) { p.Traffic = nil }, "requires a traffic snapshot"},
		{"missing-ssh", func(p *ClientCreatePayload) { p.Client.SSH = nil }, ErrClientPolicyUnsupported.Error()},
	} {
		t.Run(test.name, func(t *testing.T) {
			portableTestSchema(t)
			record := clientPolicyFixture(t)
			svc := &ClientService{}
			items := portableBrowserRoundTrip(t, svc)
			item := items[0]
			item.Client.Email, item.Client.SubID = "invalid-policy", "invalid-policy-sub"
			test.change(&item)
			result, restart, err := svc.ImportClients(&InboundService{}, []ClientCreatePayload{item})
			if err != nil || restart || result.Created != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, test.reason) {
				t.Errorf("invalid snapshot result=%+v restart=%t err=%v", result, restart, err)
			}
			assertPortableImportAbsent(t, item.Client.Email)
			members, err := svc.ListForInbound(nil, items[0].InboundIds[0])
			if err != nil || len(members) != 1 || members[0].Email != record.Email {
				t.Fatalf("invalid restore changed membership: %+v err=%v", members, err)
			}
		})
	}
}

func TestPortablePolicyCannotBeIgnoredByCreateEndpoints(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "bulk"}[bulk], func(t *testing.T) {
			portableTestSchema(t)
			setupBulkDB(t)
			ib := mkInbound(t, 25109, model.VLESS, `{"clients":[]}`)
			item := ClientCreatePayload{
				Client: model.Client{Email: "policy@wrong-api", Enable: true}, InboundIds: []int{ib.Id},
				Policy: &ClientPortablePolicy{FormatVersion: 1, Scope: "local", Multiplier: "2", Billed: "0", TrafficTotal: "0", TrafficEnable: true},
			}
			svc := &ClientService{}
			if bulk {
				result, _, err := svc.BulkCreate(&InboundService{}, []ClientCreatePayload{item})
				if err != nil || result.Created != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "import endpoint") {
					t.Errorf("bulk silently dropped policy: result=%+v err=%v", result, err)
				}
			} else {
				_, err := svc.Create(&InboundService{}, &item)
				if err == nil || !strings.Contains(err.Error(), "import endpoint") {
					t.Errorf("create silently dropped policy: %v", err)
				}
			}
			assertPortableImportAbsent(t, item.Client.Email, ib.Id)
		})
	}
}

func TestPortablePolicyPreservesSegmentedCharges(t *testing.T) {
	portableTestSchema(t)
	record := clientPolicyFixture(t)
	db := database.GetDB()
	ctx, svc := context.Background(), &ClientService{}
	policy, err := svc.UpdatePolicy(ctx, record.Email, ClientPolicyUpdate{PolicyID: record.PolicyID, UploadBps: 65536, DownloadBps: 131072, Scope: "local", Multiplier: "0.5"})
	if err != nil {
		t.Fatal(err)
	}
	ledger := database.NewClientUsageLedger(db)
	meter, err := ledger.ClaimAdmissionSource(ctx, record.PolicyID, "portable/segmented")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(ctx, database.ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 3}); err != nil {
		t.Fatal(err)
	}
	request := policy.ClientPolicyUpdate
	request.Multiplier = "2"
	if _, err := svc.UpdatePolicy(ctx, record.Email, request); err != nil {
		t.Fatal(err)
	}
	meter, err = ledger.ClaimAdmissionSource(ctx, record.PolicyID, "portable/segmented")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(ctx, database.ClientUsageReport{MeterID: meter.ID, Sequence: 1, Down: 3}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Update("total", 9).Error; err != nil {
		t.Fatal(err)
	}
	items := portableBrowserRoundTrip(t, svc)
	if _, err := svc.Delete(&InboundService{}, record.Id, false); err != nil {
		t.Fatal(err)
	}
	result, _, err := svc.ImportClients(&InboundService{}, items)
	if err != nil || result.Created != 1 || len(result.Skipped) != 0 {
		t.Fatalf("import=%+v err=%v", result, err)
	}
	got, err := svc.GetPolicy(ctx, record.Email)
	if err != nil || got.UploadBps != 65536 || got.DownloadBps != 131072 || got.Multiplier != "2" || got.Scope != "local" || got.Usage.Up != "3" || got.Usage.Down != "3" || got.Usage.Billed != "7" || got.Usage.Remainder != 500 || got.Usage.Quota != "9" {
		t.Fatalf("portable round trip changed policy/history: %+v err=%v", got, err)
	}
	if got.PolicyID == record.PolicyID {
		t.Fatal("import reused an old accounting identity")
	}
	newMeter, err := ledger.ClaimAdmissionSource(ctx, got.PolicyID, "portable/segmented")
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.CheckAdmissionSource(ctx, newMeter.ID); !errors.Is(err, database.ErrUsageQuota) {
		t.Fatalf("restored narrow quota no longer denies admission: %v", err)
	}
	if newMeter.ID == meter.ID {
		t.Fatal("import reused an old meter lifetime")
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Update("total", 10).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(ctx, database.ClientUsageReport{MeterID: newMeter.ID, Sequence: 1, Up: 1}); err != nil {
		t.Fatal(err)
	}
	got, err = svc.GetPolicy(ctx, record.Email)
	if err != nil || got.Usage.Up != "4" || got.Usage.Down != "3" || got.Usage.Billed != "9" || got.Usage.Remainder != 500 {
		t.Fatalf("new usage did not continue at restored multiplier: %+v err=%v", got, err)
	}
}

func TestPortableBytesSurviveBrowserJSON(t *testing.T) {
	portableTestSchema(t)
	setupBulkDB(t)
	db := database.GetDB()
	client := model.Client{Email: "exact@portable", Enable: true, TotalGB: 9007199254741013}
	if err := db.Create(client.ToRecord()).Error; err != nil {
		t.Fatal(err)
	}
	traffic := xray.ClientTraffic{Email: client.Email, Enable: true, Up: 9007199254740993, Down: 3, Total: client.TotalGB}
	if err := db.Create(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	items := portableBrowserRoundTrip(t, &ClientService{})
	if len(items) != 1 || items[0].Client.TotalGB != 9007199254741013 || items[0].Traffic == nil || items[0].Traffic.Up != 9007199254740993 || items[0].Traffic.Down != 3 {
		t.Fatalf("browser rounded exported bytes: %+v", items)
	}
}

func TestPortablePolicyPreservesLargeAttachedQuotaAndCharges(t *testing.T) {
	portableTestSchema(t)
	record := clientPolicyFixture(t)
	db, svc := database.GetDB(), &ClientService{}
	if err := db.Model(&record).Update("total_gb", int64(9007199254741013)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).
		Updates(map[string]any{"total": int64(9007199254741013), "up": int64(9007199254740993), "down": 3}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ClientUsageAccount{}).Where("policy_id = ?", record.PolicyID).
		Updates(map[string]any{"up": int64(9007199254740993), "down": 3, "billed": int64(9007199254741001), "remainder": 500, "multiplier": 2000}).Error; err != nil {
		t.Fatal(err)
	}
	items := portableBrowserRoundTrip(t, svc)
	if _, err := svc.Delete(&InboundService{}, record.Id, false); err != nil {
		t.Fatal(err)
	}
	result, _, err := svc.ImportClients(&InboundService{}, items)
	if err != nil || result.Created != 1 || len(result.Skipped) != 0 {
		t.Fatalf("import=%+v err=%v", result, err)
	}
	got, err := svc.GetPolicy(context.Background(), record.Email)
	if err != nil || got.Usage.Up != "9007199254740993" || got.Usage.Down != "3" || got.Usage.Billed != "9007199254741001" || got.Usage.Quota != "9007199254741013" || got.Usage.Remainder != 500 || got.Multiplier != "2" {
		t.Fatalf("attached import rounded exact byte values: %+v err=%v", got, err)
	}
}

func TestPortablePolicyReplacesRetainedTrafficWithoutReusingMeters(t *testing.T) {
	portableTestSchema(t)
	record := clientPolicyFixture(t)
	db, svc, ctx := database.GetDB(), &ClientService{}, context.Background()
	if _, err := svc.UpdatePolicy(ctx, record.Email, ClientPolicyUpdate{PolicyID: record.PolicyID, UploadBps: 65536, Scope: "local", Multiplier: "2"}); err != nil {
		t.Fatal(err)
	}
	ledger := database.NewClientUsageLedger(db)
	meter, err := ledger.ClaimAdmissionSource(ctx, record.PolicyID, "portable/retained")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(ctx, database.ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 3}); err != nil {
		t.Fatal(err)
	}
	items := portableBrowserRoundTrip(t, svc)
	if _, err := svc.Delete(&InboundService{}, record.Id, true); err != nil {
		t.Fatal(err)
	}
	traffic := trafficOf(t, record.Email)
	if traffic.PolicyID != record.PolicyID || traffic.Up != 3 {
		t.Fatalf("fixture did not retain old traffic: %+v", traffic)
	}
	result, _, err := svc.ImportClients(&InboundService{}, items)
	if err != nil || result.Created != 1 || len(result.Skipped) != 0 {
		t.Fatalf("retained traffic prevented explicit restoration: %+v err=%v", result, err)
	}
	if _, err := ledger.Admit(ctx, database.ClientUsageReport{MeterID: meter.ID, Sequence: 2, Up: 4}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("old meter can still report after identity replacement: %v", err)
	}
	got, err := svc.GetPolicy(ctx, record.Email)
	if err != nil || got.PolicyID == record.PolicyID || got.UploadBps != 65536 || got.Multiplier != "2" || got.Usage.Up != "3" || got.Usage.Billed != "6" {
		t.Fatalf("restoration inherited/replayed the old identity: %+v err=%v", got, err)
	}
}

func TestPortablePolicyPreservesIndependentRestrictions(t *testing.T) {
	for _, test := range []struct {
		name                          string
		clientEnabled, trafficEnabled bool
		clientExpiry, trafficExpiry   int64
		want                          error
	}{
		{"manual-disable", false, true, 0, 0, database.ErrUsageDisabled},
		{"traffic-disable", true, false, 0, 0, database.ErrUsageDisabled},
		{"client-expiry", true, true, 1, 0, database.ErrUsageExpired},
		{"traffic-expiry", true, true, 0, 1, database.ErrUsageExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			portableTestSchema(t)
			record := clientPolicyFixture(t)
			db, svc, ctx := database.GetDB(), &ClientService{}, context.Background()
			if err := db.Model(&record).Updates(map[string]any{"enable": test.clientEnabled, "expiry_time": test.clientExpiry}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).
				Updates(map[string]any{"enable": test.trafficEnabled, "expiry_time": test.trafficExpiry}).Error; err != nil {
				t.Fatal(err)
			}
			items := portableBrowserRoundTrip(t, svc)
			if _, err := svc.Delete(&InboundService{}, record.Id, false); err != nil {
				t.Fatal(err)
			}
			result, _, err := svc.ImportClients(&InboundService{}, items)
			if err != nil || result.Created != 1 || len(result.Skipped) != 0 {
				t.Fatalf("restriction restore=%+v err=%v", result, err)
			}
			restored := lookupClientRecord(t, record.Email)
			traffic := trafficOf(t, record.Email)
			if restored.Enable != test.clientEnabled || restored.ExpiryTime != test.clientExpiry || traffic.Enable != test.trafficEnabled || traffic.ExpiryTime != test.trafficExpiry {
				t.Fatalf("independent restriction flags changed: client=%+v traffic=%+v", restored, traffic)
			}
			ledger := database.NewClientUsageLedger(db)
			meter, err := ledger.ClaimAdmissionSource(ctx, restored.PolicyID, "portable/restrictions")
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.CheckAdmissionSource(ctx, meter.ID); !errors.Is(err, test.want) {
				t.Fatalf("restriction lost on restore: %v, want %v", err, test.want)
			}
		})
	}
}

func portableBrowserRoundTrip(t *testing.T, svc *ClientService) []ClientCreatePayload {
	t.Helper()
	items, err := svc.ExportAll()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var browser any
	if err := json.Unmarshal(encoded, &browser); err != nil {
		t.Fatal(err)
	}
	for _, item := range browser.([]any) {
		if _, ok := item.(map[string]any)["inboundIds"].([]any); !ok {
			t.Fatal("portable export omitted the attachment array required by its API schema")
		}
	}
	encoded, err = json.Marshal(browser)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &items); err != nil {
		t.Fatal(err)
	}
	return items
}
