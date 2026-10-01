package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestLegacyUnassignedTrafficLateMatchingRowKeepsCapturedClassification(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	const label = "late-created-wire-label"
	const hook = "test-retained-late-match"
	inserted := false
	if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "legacy_unassigned_traffics" && !inserted {
			inserted = true
			tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Create(&xray.ClientTraffic{Email: label, Enable: true, Up: 31, Down: 37}).Error)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(hook) })
	svc := &XrayService{}
	batch := &xray.TrafficBatch{
		ProcessID: "late-label-source", Sequence: 1, ID: "late-label-first", SourceMode: "legacy",
		ClientTraffics: []*xray.ClientTraffic{{Email: label, Up: 7, Down: 11}},
	}
	if err := svc.settleLegacyTrafficBatch(batch); err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatal("late matching row probe was not reached")
	}
	_ = db.Callback().Create().Remove(hook)
	if got := trafficOf(t, label); got.Up != 31 || got.Down != 37 {
		t.Fatalf("late matching row adopted an already-unassigned batch: %+v", got)
	}
	batch.Sequence, batch.ID = 2, "late-label-next"
	batch.ClientTraffics[0].Up, batch.ClientTraffics[0].Down = 2, 3
	if err := svc.settleLegacyTrafficBatch(batch); err != nil {
		t.Fatal(err)
	}
	if got := trafficOf(t, label); got.Up != 33 || got.Down != 40 {
		t.Fatalf("later matching row missed new delta: %+v", got)
	}
	var bucket model.LegacyUnassignedTraffic
	if err := db.First(&bucket).Error; err != nil || bucket.RawUpload != 7 || bucket.RawDownload != 11 {
		t.Fatalf("late matching row rewrote retained history: %+v/%v", bucket, err)
	}
}

func TestLegacyUnassignedTrafficManyLabelsLateFailureAndRetry(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	known := passwordOwner(t, "many-retained-known")
	if err := db.Create(&xray.ClientTraffic{Email: known.Email, Enable: true, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	batch := &xray.TrafficBatch{
		ProcessID: "many-retained-source", Sequence: 1, ID: "many-retained-batch", SourceMode: "managed", SourceInstanceID: "captured-managed-instance",
		ClientTraffics: []*xray.ClientTraffic{{Email: known.Email, Up: 3, Down: 5}},
	}
	const labels = 601
	for i := range labels {
		batch.ClientTraffics = append(batch.ClientTraffics, &xray.ClientTraffic{Email: fmt.Sprintf("wire-%04d", i), Up: int64(i + 1), Down: int64(i + 2)})
	}
	injected := errors.New("later retained-label batch unavailable")
	const hook = "test-retained-many-failure"
	writes := 0
	if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "legacy_unassigned_traffics" {
			writes++
			if writes == 3 {
				tx.AddError(injected)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(hook) })
	svc := &XrayService{}
	if err := svc.settleLegacyTrafficBatch(batch); !errors.Is(err, injected) || writes != 3 {
		t.Fatalf("later chunk did not roll back: writes=%d/%v", writes, err)
	}
	for _, table := range []any{&model.LegacyUnassignedTraffic{}, &model.LegacyTrafficReceipt{}} {
		var count int64
		if err := db.Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("later chunk left earlier writes: %T %d/%v", table, count, err)
		}
	}
	if got := trafficOf(t, known.Email); got.Up != 100 || got.Down != 200 {
		t.Fatalf("failed retained chunk changed known sibling: %+v", got)
	}
	_ = db.Callback().Create().Remove(hook)
	for range 2 {
		if err := svc.settleLegacyTrafficBatch(batch); err != nil {
			t.Fatal(err)
		}
	}
	var retained []model.LegacyUnassignedTraffic
	if err := db.Order("label").Find(&retained).Error; err != nil || len(retained) != labels {
		t.Fatalf("bounded label writes lost data: %d/%v", len(retained), err)
	}
	for i, row := range retained {
		if row.Label != fmt.Sprintf("wire-%04d", i) || row.RawUpload != int64(i+1) || row.RawDownload != int64(i+2) || row.SourceMode != "managed" || row.SourceInstanceID != batch.SourceInstanceID {
			t.Fatalf("bounded retry changed raw source evidence: %+v", row)
		}
	}
	if got := trafficOf(t, known.Email); got.Up != 103 || got.Down != 205 {
		t.Fatalf("retry replayed known sibling: %+v", got)
	}
}

func TestLegacyUnassignedTrafficPreservesNULAndLiteralEscapeLabels(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	svc := &XrayService{}
	labels := []string{"legacy\x00username", `legacy\u0000username`, `"legacy\u0000username"`, "bGVnYWN5AHVzZXJuYW1l"}
	batch := &xray.TrafficBatch{ProcessID: "nul-label-source", Sequence: 1, ID: "nul-label-first", SourceMode: "legacy"}
	for i, label := range labels {
		batch.ClientTraffics = append(batch.ClientTraffics, &xray.ClientTraffic{Email: label, Up: int64(7 + i), Down: int64(11 + i)})
	}
	for range 2 {
		if err := svc.settleLegacyTrafficBatch(batch); err != nil {
			t.Fatalf("admitted exact NUL label stalled settlement: %v", err)
		}
	}
	var retained []model.LegacyUnassignedTraffic
	if err := db.Find(&retained).Error; err != nil || len(retained) != len(labels) {
		t.Fatalf("NUL label or literal alias lost: %d/%v", len(retained), err)
	}
	for i, label := range labels {
		found := false
		for _, row := range retained {
			if row.Label == label && row.SourceMode == "legacy" && row.RawUpload == int64(7+i) && row.RawDownload == int64(11+i) {
				found = true
			}
		}
		if !found {
			t.Fatalf("NUL label or literal alias changed: %q", label)
		}
	}
	batch.Sequence, batch.ID = 2, "nul-label-growth"
	batch.ClientTraffics = []*xray.ClientTraffic{{Email: labels[0], Up: 2, Down: 3}}
	if err := svc.settleLegacyTrafficBatch(batch); err != nil {
		t.Fatal(err)
	}
	var current []model.LegacyUnassignedTraffic
	if err := db.Find(&current).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range current {
		if row.Label == labels[0] && (row.RawUpload != 9 || row.RawDownload != 14) {
			t.Fatalf("later growth replayed original NUL delta: %+v", row)
		}
	}
}

func TestLegacyUnassignedTrafficRetainsExactLabelsAndRetries(t *testing.T) {
	setupPolicyLedgerDB(t)
	known := passwordOwner(t, "legacy-retention-known")
	db := database.GetDB()
	if err := db.Create(&xray.ClientTraffic{Email: known.Email, Enable: true, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("用户名", 1500)
	batch := &xray.TrafficBatch{ProcessID: "retention-source", Sequence: 1, ID: "retention-batch", ClientTraffics: []*xray.ClientTraffic{
		{Email: known.Email, Up: 3, Down: 5},
		{Email: "alice", Up: 7, Down: 11},
		{Email: "Alice", Up: 13, Down: 17},
		{Email: long, Up: 19, Down: 23},
	}}
	svc := &XrayService{}
	for range 2 {
		if err := svc.settleLegacyTrafficBatch(batch); err != nil {
			t.Fatal(err)
		}
	}
	var retained []model.LegacyUnassignedTraffic
	if err := db.Table("legacy_unassigned_traffics").Where("process_id = ?", batch.ProcessID).Order("label").Find(&retained).Error; err != nil {
		t.Fatal(err)
	}
	totals := make(map[string][2]int64)
	for _, row := range retained {
		totals[row.Label] = [2]int64{row.RawUpload, row.RawDownload}
	}
	if len(totals) != 3 || totals["alice"] != [2]int64{7, 11} || totals["Alice"] != [2]int64{13, 17} || totals[long] != [2]int64{19, 23} {
		t.Fatalf("unmatched labels lost/merged/replayed: %+v", retained)
	}
	if traffic := trafficOf(t, known.Email); traffic.Up != 103 || traffic.Down != 205 {
		t.Fatalf("known sibling replayed: %+v", traffic)
	}
	var receipt struct{ PayloadDigest string }
	if err := db.Table("legacy_traffic_receipts").Where("process_id = ?", batch.ProcessID).Take(&receipt).Error; err != nil || len(receipt.PayloadDigest) != 64 {
		t.Fatalf("missing original payload binding: %+v/%v", receipt, err)
	}
	altered := *batch
	altered.ClientTraffics = []*xray.ClientTraffic{{Email: "alice", Up: 800, Down: 900}}
	if err := svc.settleLegacyTrafficBatch(&altered); err == nil {
		t.Fatal("changed payload accepted under committed receipt identity")
	}
	if err := db.Create(&xray.ClientTraffic{Email: "alice", Enable: true, Up: 31, Down: 37}).Error; err != nil {
		t.Fatal(err)
	}
	next := &xray.TrafficBatch{ProcessID: batch.ProcessID, Sequence: 2, ID: "retention-next", ClientTraffics: []*xray.ClientTraffic{{Email: "alice", Up: 2, Down: 3}, {Email: "Alice", Up: 5, Down: 7}}}
	if err := svc.settleLegacyTrafficBatch(next); err != nil {
		t.Fatal(err)
	}
	if traffic := trafficOf(t, "alice"); traffic.Up != 33 || traffic.Down != 40 {
		t.Fatalf("later label recycled retained historical usage: %+v", traffic)
	}
	if err := db.Table("legacy_unassigned_traffics").Where("process_id = ?", batch.ProcessID).Find(&retained).Error; err != nil {
		t.Fatal(err)
	}
	totals = make(map[string][2]int64)
	for _, row := range retained {
		totals[row.Label] = [2]int64{row.RawUpload, row.RawDownload}
	}
	if totals["alice"] != [2]int64{7, 11} || totals["Alice"] != [2]int64{18, 24} {
		t.Fatalf("retained provenance moved by mutable label: %+v", totals)
	}
}

func TestLegacyUnassignedTrafficLateFailureRollsBackAllLayers(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	inbound := mkInbound(t, 24841, model.VLESS, `{"decryption":"none","clients":[]}`)
	if err := db.Model(inbound).Updates(map[string]any{"tag": "retention-inbound", "up": int64(100), "down": int64(200)}).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("late retained-label write unavailable")
	const hook = "test-retention-late-failure"
	if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "legacy_unassigned_traffics" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(hook) })
	batch := &xray.TrafficBatch{ProcessID: "retention-rollback", Sequence: 1, ID: "retention-rollback-batch", Traffics: []*xray.Traffic{{IsInbound: true, Tag: "retention-inbound", Up: 3, Down: 5}}, ClientTraffics: []*xray.ClientTraffic{{Email: "wire-only", Up: 7, Down: 11}}}
	if err := (&XrayService{}).settleLegacyTrafficBatch(batch); !errors.Is(err, injected) {
		t.Fatalf("late bucket failure not returned: %v", err)
	}
	var current model.Inbound
	if err := db.First(&current, inbound.Id).Error; err != nil || current.Up != 100 || current.Down != 200 {
		t.Fatalf("late retention error committed inbound: %+v/%v", current, err)
	}
	var count int64
	if err := db.Table("legacy_traffic_receipts").Where("process_id = ?", batch.ProcessID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed retention advanced receipt: %d/%v", count, err)
	}
}
