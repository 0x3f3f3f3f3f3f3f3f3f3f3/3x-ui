package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type legacyConfigSourceProbe struct {
	ProcessID             string
	ConfigDigest          string
	EffectiveConfigDigest string
	ConfigStable          bool
}

func provenanceBatch(process string) *xray.TrafficBatch {
	return &xray.TrafficBatch{
		ProcessID: process, Sequence: 1, ID: "proof-first-batch", SourceMode: "legacy",
		ConfigProof:    &xray.TrafficConfigProof{ConfigDigest: strings.Repeat("ab", 32), EffectiveConfigDigest: strings.Repeat("cd", 32), ConfigStable: true},
		ClientTraffics: []*xray.ClientTraffic{{Email: "proof-unassigned", Up: 7, Down: 11}},
	}
}

func configSourceOf(t *testing.T, process string) legacyConfigSourceProbe {
	t.Helper()
	var source legacyConfigSourceProbe
	if err := database.GetDB().Table("legacy_traffic_config_sources").First(&source, "process_id = ?", process).Error; err != nil {
		t.Fatalf("source startup evidence is not durable: %v", err)
	}
	return source
}

func TestLegacyTrafficConfigProofOriginalDigestReplay(t *testing.T) {
	setupPolicyLedgerDB(t)
	// Golden SHA from the Task5B8A intent encoding, before adding ConfigProof.
	const originalDigest = "8b9f6a84f47682093860a2a41cc83708435f6e74454285de7b2d497bf6822efa"
	batch := &xray.TrafficBatch{
		ProcessID: "proof-old-source", Sequence: 1, ID: "proof-old-batch", SourceMode: "legacy",
		ClientTraffics: []*xray.ClientTraffic{{Email: "old-wire", Up: 7, Down: 11}},
	}
	normalized, err := normalizeLegacyTrafficBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := legacyTrafficPayloadDigest(normalized); err != nil || digest != originalDigest {
		t.Fatalf("nil proof changed authentic previous receipt: %s/%v", digest, err)
	}
	db := database.GetDB()
	want := model.LegacyTrafficReceipt{ProcessID: batch.ProcessID, Sequence: batch.Sequence, BatchID: batch.ID, PayloadDigest: originalDigest}
	if err := db.Create(&want).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&XrayService{}).settleLegacyTrafficBatch(batch); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("legacy_traffic_config_sources").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("old receipt replay fabricated configuration evidence: %d/%v", count, err)
	}
	if err := db.Model(&model.LegacyUnassignedTraffic{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("old receipt replay created new raw history: %d/%v", count, err)
	}
}

func TestLegacyTrafficConfigProofDurableMonotonicAndImmutable(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &XrayService{}
	batch := provenanceBatch("proof-source")
	if err := svc.settleLegacyTrafficBatch(batch); err != nil {
		t.Fatal(err)
	}
	before := configSourceOf(t, batch.ProcessID)
	if !before.ConfigStable || before.ConfigDigest != batch.ConfigProof.ConfigDigest || before.EffectiveConfigDigest != batch.ConfigProof.EffectiveConfigDigest {
		t.Fatalf("source did not retain original proof: %+v", before)
	}
	for i, stable := range []bool{false, true} {
		batch.Sequence, batch.ID, batch.ConfigProof.ConfigStable = int64(i+2), "proof-next-"+strings.Repeat("x", i+1), stable
		if err := svc.settleLegacyTrafficBatch(batch); err != nil {
			t.Fatal(err)
		}
		if after := configSourceOf(t, batch.ProcessID); after.ConfigStable || after.ConfigDigest != before.ConfigDigest || after.EffectiveConfigDigest != before.EffectiveConfigDigest {
			t.Fatalf("source restored stability or rewrote startup identity: %+v", after)
		}
	}
	batch.Sequence, batch.ID = 4, "proof-conflicting-digest"
	batch.ConfigProof.ConfigDigest = strings.Repeat("ef", 32)
	if err := svc.settleLegacyTrafficBatch(batch); !errors.Is(err, ErrLegacyTrafficRetention) {
		t.Fatalf("same child acquired a different configuration identity: %v", err)
	}
	var bucket model.LegacyUnassignedTraffic
	if err := database.GetDB().First(&bucket).Error; err != nil || bucket.RawUpload != 21 || bucket.RawDownload != 33 {
		t.Fatalf("rejected proof changed raw counters: %+v/%v", bucket, err)
	}
	unknown := provenanceBatch("proof-unknown-source")
	unknown.ConfigProof = nil
	if err := svc.settleLegacyTrafficBatch(unknown); err != nil {
		t.Fatal(err)
	}
	if source := configSourceOf(t, unknown.ProcessID); source.ConfigStable || source.ConfigDigest != "" || source.EffectiveConfigDigest != "" {
		t.Fatalf("missing proof acquired fabricated identity: %+v", source)
	}
	unknown.Sequence, unknown.ID, unknown.ConfigProof = 2, "proof-promote-unknown", provenanceBatch("unused").ConfigProof
	if err := svc.settleLegacyTrafficBatch(unknown); !errors.Is(err, ErrLegacyTrafficRetention) {
		t.Fatalf("later config promoted an unknown historical source: %v", err)
	}
}

func TestLegacyTrafficConfigProofReceiptRejectsChangedProof(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &XrayService{}
	batch := provenanceBatch("proof-retry-source")
	if err := svc.settleLegacyTrafficBatch(batch); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*xray.TrafficConfigProof){
		func(proof *xray.TrafficConfigProof) { proof.ConfigStable = false },
		func(proof *xray.TrafficConfigProof) { proof.ConfigDigest = strings.Repeat("ef", 32) },
		func(proof *xray.TrafficConfigProof) { proof.EffectiveConfigDigest = strings.Repeat("ef", 32) },
	} {
		changed := *batch
		proof := *batch.ConfigProof
		changed.ConfigProof = &proof
		mutate(changed.ConfigProof)
		if err := svc.settleLegacyTrafficBatch(&changed); !errors.Is(err, ErrLegacyTrafficBatch) {
			t.Fatalf("committed tuple accepted changed startup evidence: %v", err)
		}
	}
	changed := *batch
	changed.ConfigProof = nil
	if err := svc.settleLegacyTrafficBatch(&changed); !errors.Is(err, ErrLegacyTrafficBatch) {
		t.Fatalf("committed tuple erased original startup evidence: %v", err)
	}
}

func TestLegacyTrafficConfigProofValidationAndDetachedIntent(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &XrayService{}
	for _, proof := range []*xray.TrafficConfigProof{
		{},
		{ConfigDigest: strings.Repeat("ab", 32)},
		{ConfigDigest: strings.Repeat("AB", 32), EffectiveConfigDigest: strings.Repeat("cd", 32)},
		{ConfigDigest: strings.Repeat("gg", 32), EffectiveConfigDigest: strings.Repeat("cd", 32)},
	} {
		batch := provenanceBatch("invalid-proof-source")
		batch.ConfigProof = proof
		if err := svc.settleLegacyTrafficBatch(batch); !errors.Is(err, ErrLegacyTrafficBatch) {
			t.Fatalf("malformed startup proof accepted: %+v/%v", proof, err)
		}
	}
	batch := provenanceBatch("detached-proof-source")
	if err := svc.settleLegacyTrafficBatchChecked(batch, func(_ *gorm.DB, checked *xray.TrafficBatch) error {
		checked.ConfigProof.ConfigStable = false
		return nil
	}); !errors.Is(err, ErrLegacyTrafficBatch) || !batch.ConfigProof.ConfigStable {
		t.Fatalf("validator changed caller proof or committed altered intent: stable=%t/%v", batch.ConfigProof.ConfigStable, err)
	}
}

func TestLegacyTrafficConfigProofLateRollback(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	if err := db.Create(&xray.ClientTraffic{Email: "proof-known", Enable: true, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	batch := provenanceBatch("rollback-proof-source")
	batch.ClientTraffics = append(batch.ClientTraffics, &xray.ClientTraffic{Email: "proof-known", Up: 3, Down: 5})
	svc := &XrayService{}
	injected := errors.New("configuration proof final receipt write unavailable")
	const hook = "test-config-proof-late-failure"
	install := func() {
		t.Helper()
		if err := db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
			if tx.Statement.Table == "legacy_traffic_receipts" {
				tx.AddError(injected)
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hook) })
	install()
	if err := svc.settleLegacyTrafficBatch(batch); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	for _, table := range []string{"legacy_traffic_config_sources", "legacy_unassigned_traffics", "legacy_traffic_receipts"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("late receipt failure left earlier writes: %s %d/%v", table, count, err)
		}
	}
	_ = db.Callback().Update().Remove(hook)
	if err := svc.settleLegacyTrafficBatch(batch); err != nil {
		t.Fatal(err)
	}
	batch.Sequence, batch.ID, batch.ConfigProof.ConfigStable = 2, "proof-rollback-downgrade", false
	install()
	if err := svc.settleLegacyTrafficBatch(batch); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if !configSourceOf(t, batch.ProcessID).ConfigStable {
		t.Fatal("failed next receipt committed a source stability downgrade")
	}
	if got := trafficOf(t, "proof-known"); got.Up != 103 || got.Down != 205 {
		t.Fatalf("late failure replayed known sibling: %+v", got)
	}
}

func TestLegacyTrafficConfigProofMissingHistoricalHeaderCannotPromote(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	batch := provenanceBatch("proof-old-source")
	old := model.LegacyTrafficReceipt{
		ProcessID: batch.ProcessID, Sequence: 1, BatchID: "proof-old-batch",
		PayloadDigest: "8b9f6a84f47682093860a2a41cc83708435f6e74454285de7b2d497bf6822efa",
	}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	batch.ClientTraffics[0].Email = "old-wire"
	hash := sha256.Sum256([]byte("old-wire"))
	bucket := model.LegacyUnassignedTraffic{ProcessID: batch.ProcessID, LabelHash: hex.EncodeToString(hash[:]), Label: "old-wire", SourceMode: "legacy", RawUpload: 7, RawDownload: 11}
	if err := db.Create(&bucket).Error; err != nil {
		t.Fatal(err)
	}
	batch.Sequence, batch.ID = 2, "proof-old-next"
	if err := (&XrayService{}).settleLegacyTrafficBatch(batch); !errors.Is(err, ErrLegacyTrafficRetention) {
		t.Fatalf("later proof promoted historical traffic lacking a source header: %v", err)
	}
	var count int64
	if err := db.Table("legacy_traffic_config_sources").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rejected historical promotion invented evidence: %d/%v", count, err)
	}
	var got model.LegacyUnassignedTraffic
	if err := db.First(&got).Error; err != nil || got != bucket {
		t.Fatalf("rejected historical promotion rewrote authentic raw history: %+v/%v", got, err)
	}
	var receipt model.LegacyTrafficReceipt
	if err := db.First(&receipt).Error; err != nil || receipt != old {
		t.Fatalf("rejected historical promotion changed original receipt: %+v/%v", receipt, err)
	}
	batch.ConfigProof = nil
	if err := (&XrayService{}).settleLegacyTrafficBatch(batch); err != nil {
		t.Fatalf("ordinary unknown historical counters stopped settling: %v", err)
	}
	if source := configSourceOf(t, batch.ProcessID); source.ConfigStable || source.ConfigDigest != "" || source.EffectiveConfigDigest != "" {
		t.Fatalf("historical unknown receipt acquired proof: %+v", source)
	}
	if err := db.First(&got).Error; err != nil || got.RawUpload != 14 || got.RawDownload != 22 {
		t.Fatalf("ordinary unknown historical growth was lost: %+v/%v", got, err)
	}
}
