package service

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestLegacyUnassignedTrafficRejectsInvalidInputBeforeWrites(t *testing.T) {
	for _, invalid := range []string{"process-utf8", "batch-utf8", "instance-utf8", "process-length", "instance-length", "unknown-instance", "negative-client", "duplicate-client", "duplicate-inbound", "conflicting-direction"} {
		t.Run(invalid, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			batch := &xray.TrafficBatch{
				ProcessID: "input-source", Sequence: 1, ID: "input-batch",
				ClientTraffics: []*xray.ClientTraffic{{Email: "wire-only", Up: 7, Down: 11}},
			}
			switch invalid {
			case "process-utf8":
				batch.ProcessID = string([]byte{0xff})
			case "batch-utf8":
				batch.ID = string([]byte{0xff})
			case "instance-utf8":
				batch.SourceMode, batch.SourceInstanceID = "managed", string([]byte{0xff})
			case "process-length":
				batch.ProcessID = strings.Repeat("a", 37)
			case "instance-length":
				batch.SourceMode, batch.SourceInstanceID = "managed", strings.Repeat("a", 37)
			case "unknown-instance":
				batch.SourceInstanceID = "invented-instance"
			case "negative-client":
				batch.ClientTraffics[0].Down = -1
			case "duplicate-client":
				batch.ClientTraffics = append(batch.ClientTraffics, &xray.ClientTraffic{Email: "wire-only", Up: 1})
			case "duplicate-inbound":
				batch.Traffics = []*xray.Traffic{{IsInbound: true, Tag: "in", Up: 1}, {IsInbound: true, Tag: "in", Up: 2}}
			case "conflicting-direction":
				batch.Traffics = []*xray.Traffic{{IsInbound: true, IsOutbound: true, Tag: "in", Up: 1}}
			}
			if err := (&XrayService{}).settleLegacyTrafficBatch(batch); !errors.Is(err, ErrLegacyTrafficBatch) {
				t.Fatalf("invalid original intent not rejected before SQL: %v", err)
			}
			for _, table := range []any{&model.LegacyTrafficReceipt{}, &model.LegacyUnassignedTraffic{}} {
				var count int64
				if err := database.GetDB().Model(table).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("invalid original intent persisted: %T %d/%v", table, count, err)
				}
			}
		})
	}
}

func TestLegacyUnassignedTrafficValidatorCannotRewriteIntent(t *testing.T) {
	for _, mutation := range []string{"amount", "nil-client"} {
		t.Run(mutation, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			inbound := mkInbound(t, 24842, model.VLESS, `{"decryption":"none","clients":[]}`)
			batch := &xray.TrafficBatch{
				ProcessID: "retention-validator", Sequence: 1, ID: "original-intent",
				ClientTraffics: []*xray.ClientTraffic{{Email: "wire-only", Up: 7, Down: 11}},
			}
			err := (&XrayService{}).settleLegacyTrafficBatchChecked(batch, func(tx *gorm.DB, checked *xray.TrafficBatch) error {
				if err := tx.Model(inbound).Update("up", 201).Error; err != nil {
					return err
				}
				if mutation == "amount" {
					checked.ClientTraffics[0].Up = 900
				} else {
					checked.ClientTraffics[0] = nil
				}
				return nil
			})
			if !errors.Is(err, ErrLegacyTrafficBatch) {
				t.Fatalf("validator rewrite was not explicitly rejected: %v", err)
			}
			if batch.ClientTraffics[0] == nil || batch.ClientTraffics[0].Up != 7 {
				t.Fatal("validator changed caller's retry snapshot")
			}
			var current model.Inbound
			if err := database.GetDB().First(&current, inbound.Id).Error; err != nil || current.Up != 0 {
				t.Fatalf("failed intent check committed preceding write: %+v/%v", current, err)
			}
		})
	}
}

func TestLegacyUnassignedTrafficRejectsCorruptOrOverflowingBucket(t *testing.T) {
	for _, corruption := range []string{"upload-overflow", "download-overflow", "negative-stored", "label-collision", "source-mode", "source-instance"} {
		t.Run(corruption, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			svc := &XrayService{}
			first := &xray.TrafficBatch{
				ProcessID: "retention-guards", Sequence: 1, ID: "guard-first",
				ClientTraffics: []*xray.ClientTraffic{{Email: "wire-only", Up: 7, Down: 11}},
			}
			if corruption == "source-instance" {
				first.SourceMode, first.SourceInstanceID = "managed", "instance-a"
			}
			if err := svc.settleLegacyTrafficBatch(first); err != nil {
				t.Fatal(err)
			}
			var bucket model.LegacyUnassignedTraffic
			if err := db.First(&bucket).Error; err != nil {
				t.Fatal(err)
			}
			next := &xray.TrafficBatch{
				ProcessID: first.ProcessID, Sequence: 2, ID: "guard-next",
				ClientTraffics: []*xray.ClientTraffic{{Email: "wire-only", Up: 5, Down: 6}},
			}
			var updates map[string]any
			switch corruption {
			case "upload-overflow":
				updates = map[string]any{"raw_upload": int64(math.MaxInt64)}
			case "download-overflow":
				updates = map[string]any{"raw_download": int64(math.MaxInt64)}
			case "negative-stored":
				updates = map[string]any{"raw_upload": int64(-1)}
			case "label-collision":
				bucket.Label = "different-original-label"
				if err := db.Save(&bucket).Error; err != nil {
					t.Fatal(err)
				}
			case "source-mode":
				next.SourceMode = "legacy"
			case "source-instance":
				next.SourceMode, next.SourceInstanceID = "managed", "instance-b"
			}
			if len(updates) != 0 {
				if err := db.Model(&bucket).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.First(&bucket).Error; err != nil {
				t.Fatal(err)
			}
			if err := svc.settleLegacyTrafficBatch(next); !errors.Is(err, ErrLegacyTrafficRetention) {
				t.Fatalf("invalid retained state accepted: %v", err)
			}
			var current model.LegacyUnassignedTraffic
			if err := db.First(&current).Error; err != nil || !reflect.DeepEqual(current, bucket) {
				t.Fatalf("failed retained addition changed original evidence: %+v/%v", current, err)
			}
			var receipt model.LegacyTrafficReceipt
			if err := db.First(&receipt, "process_id = ?", first.ProcessID).Error; err != nil || receipt.Sequence != 1 || receipt.BatchID != first.ID {
				t.Fatalf("failed retained addition advanced receipt: %+v/%v", receipt, err)
			}
		})
	}
}

func TestLegacyUnassignedTrafficReceiptBindsOriginalIntent(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &XrayService{}
	batch := &xray.TrafficBatch{
		ProcessID: "digest-source", Sequence: 1, ID: "digest-batch", SourceMode: "legacy",
		Traffics:       []*xray.Traffic{{IsInbound: true, Tag: "same-tag", Up: 3}, {IsOutbound: true, Tag: "same-tag", Down: 5}},
		ClientTraffics: []*xray.ClientTraffic{{Email: "alice", Up: 7, Down: 11}, {Email: "ALICE", Up: 13, Down: 17}},
	}
	if err := svc.settleLegacyTrafficBatch(batch); err != nil {
		t.Fatal(err)
	}
	// Ordering and display metadata cannot change the accounting intent.
	reordered := *batch
	reordered.Traffics = []*xray.Traffic{batch.Traffics[1], batch.Traffics[0]}
	reordered.ClientTraffics = []*xray.ClientTraffic{batch.ClientTraffics[1], batch.ClientTraffics[0]}
	if err := svc.settleLegacyTrafficBatch(&reordered); err != nil {
		t.Fatalf("same original intent in another order rejected: %v", err)
	}
	for _, mutation := range []string{"client-label", "client-amount", "network-amount", "network-direction", "final", "source"} {
		t.Run(mutation, func(t *testing.T) {
			changed := *batch
			client, traffic := *batch.ClientTraffics[0], *batch.Traffics[0]
			changed.ClientTraffics = []*xray.ClientTraffic{&client, batch.ClientTraffics[1]}
			changed.Traffics = []*xray.Traffic{&traffic, batch.Traffics[1]}
			switch mutation {
			case "client-label":
				client.Email = "renamed-wire-label"
			case "client-amount":
				client.Down++
			case "network-amount":
				traffic.Up++
			case "network-direction":
				traffic.IsInbound, traffic.IsOutbound, traffic.Tag = false, true, "other-tag"
			case "final":
				changed.Final = true
			case "source":
				changed.SourceMode, changed.SourceInstanceID = "managed", "different-instance"
			}
			if err := svc.settleLegacyTrafficBatch(&changed); !errors.Is(err, ErrLegacyTrafficBatch) {
				t.Fatalf("changed original effect accepted under committed identity: %v", err)
			}
		})
	}
	var buckets []model.LegacyUnassignedTraffic
	if err := database.GetDB().Find(&buckets).Error; err != nil || len(buckets) != 2 {
		t.Fatalf("digest retries changed original raw evidence: %+v/%v", buckets, err)
	}
	byLabel := make(map[string]model.LegacyUnassignedTraffic, len(buckets))
	for _, bucket := range buckets {
		byLabel[bucket.Label] = bucket
	}
	if len(byLabel) != 2 || byLabel["alice"].RawUpload != 7 || byLabel["alice"].RawDownload != 11 ||
		byLabel["ALICE"].RawUpload != 13 || byLabel["ALICE"].RawDownload != 17 {
		t.Fatalf("digest retries changed original raw evidence: %+v", buckets)
	}
}

func TestLegacyUnassignedTrafficHistoricalReceiptDoesNotInventHistory(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	old := model.LegacyTrafficReceipt{ProcessID: "historical-source", Sequence: 17, BatchID: "historical-batch"}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	svc := &XrayService{}
	replay := &xray.TrafficBatch{
		ProcessID: old.ProcessID, Sequence: old.Sequence, ID: old.BatchID,
		ClientTraffics: []*xray.ClientTraffic{{Email: "unknown-old-label", Up: 700, Down: 900}},
	}
	if err := svc.settleLegacyTrafficBatch(replay); err != nil {
		t.Fatal(err)
	}
	var receipt model.LegacyTrafficReceipt
	var count int64
	if err := db.First(&receipt, "process_id = ?", old.ProcessID).Error; err != nil || receipt.PayloadDigest != "" {
		t.Fatalf("old receipt acquired an invented digest: %+v/%v", receipt, err)
	}
	if err := db.Model(&model.LegacyUnassignedTraffic{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("historical replay invented retained traffic: %d/%v", count, err)
	}
	replay.Sequence, replay.ID = 18, "first-bound-batch"
	replay.ClientTraffics[0].Up, replay.ClientTraffics[0].Down = 3, 5
	if err := svc.settleLegacyTrafficBatch(replay); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&receipt, "process_id = ?", old.ProcessID).Error; err != nil || len(receipt.PayloadDigest) != 64 {
		t.Fatalf("new sequence lacks authentic payload binding: %+v/%v", receipt, err)
	}
	var bucket model.LegacyUnassignedTraffic
	if err := db.First(&bucket).Error; err != nil || bucket.RawUpload != 3 || bucket.RawDownload != 5 || bucket.SourceMode != "unknown" {
		t.Fatalf("new sequence repriced missing historical traffic: %+v/%v", bucket, err)
	}
}
