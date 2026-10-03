package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

// Admitting contradictory resource programs would project old effects onto an
// unauthorized identity or time; exercise the same journal dispatcher as boot.
func TestManagedAuthorityInboundResetRejectsInvalidPrograms(t *testing.T) {
	for _, fault := range []string{"schema", "unknown-field", "invalid-uuid", "duplicate-uuid", "duplicate-id", "wrong-id", "wrong-membership", "wrong-time", "foreign-source", "schema1-effects", "schema1-preparation", "legacy-target", "oversized"} {
		t.Run(fault, func(t *testing.T) {
			svc, tunnel, _, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			const request = "inbound-valid-before-invalid-program"
			if err := (&ClientService{}).ResetAllClientTrafficsWithRequest(ctx, &InboundService{}, tunnel.Id, request); err != nil {
				t.Fatal(err)
			}
			cfg := managedAuthorityForProcess(currentXrayProcess()).config
			if err := svc.StopXray(); err != nil {
				t.Fatal(err)
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			capture, err := state.Journal.LookupResetOperation(authorityResetRequestKey(request))
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := state.Journal.LookupResetPreparation(capture.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			original, err := decodeAuthorityResetCapture(capture, state.Journal, state.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := decodeAuthorityResetPreparation(prepared, capture, state.Journal, state.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			original.Operation.RequestID = "inbound-invalid-" + fault
			snapshot.RequestID = original.Operation.RequestID
			for i := range snapshot.Resets {
				snapshot.Resets[i].RequestID = "batch:" + authorityResetSnapshotDigest(snapshot.RequestID)
			}
			source := state.SourceID
			switch fault {
			case "schema":
				snapshot.Schema = 99
			case "invalid-uuid":
				original.OriginalInbounds[0].StableID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
			case "duplicate-uuid":
				original.Operation.Scope = "inbound:-1"
				original.OriginalInbounds = append(original.OriginalInbounds, authorityResetInboundIdentity{ID: tunnel.Id + 1, StableID: tunnel.StableID})
			case "duplicate-id":
				original.Operation.Scope = "inbound:-1"
				original.OriginalInbounds = append(original.OriginalInbounds, authorityResetInboundIdentity{ID: tunnel.Id, StableID: uuid.NewString()})
			case "wrong-id":
				original.OriginalInbounds[0].ID++
			case "wrong-membership":
				snapshot.InboundStamps[0].StableID = uuid.NewString()
			case "wrong-time":
				snapshot.InboundStamps[0].ResetAt++
			case "foreign-source":
				source = "foreign-inbound-source"
			case "schema1-effects":
				original.Schema = 1
			case "schema1-preparation":
				snapshot.Schema = 1
			case "legacy-target":
				original.OriginalManagedIDs = nil
			case "oversized":
				original.Operation.Scope = "inbound:-1"
				original.OriginalInbounds = make([]authorityResetInboundIdentity, 100001)
				for i := range original.OriginalInbounds {
					original.OriginalInbounds[i] = authorityResetInboundIdentity{ID: i + 1, StableID: tunnel.StableID}
				}
			}
			raw, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			capture.RequestID, capture.Snapshot = authorityResetRequestKey(original.Operation.RequestID), string(raw)
			if err := state.Journal.CaptureResetOperation(capture); err != nil {
				t.Fatal(err)
			}
			raw, err = json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "unknown-field" {
				raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"Unknown":true}`)
			}
			if err := state.Journal.PrepareResetOperation(policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: string(raw)}); err != nil {
				t.Fatal(err)
			}
			db := database.GetDB()
			var before model.Inbound
			if err := db.First(&before, tunnel.Id).Error; err != nil {
				t.Fatal(err)
			}
			if err := recoverAuthorityResetCaptures(ctx, db, state.Journal, source); !errors.Is(err, ErrClientPolicyLedger) {
				t.Fatalf("unsafe original inbound program admitted %s: %v", fault, err)
			}
			var after model.Inbound
			if err := db.First(&after, tunnel.Id).Error; err != nil {
				t.Fatal(err)
			}
			if after.LastTrafficResetTime != before.LastTrafficResetTime || after.Settings != before.Settings || after.StableID != before.StableID {
				t.Fatal("rejected program mutated original resource")
			}
			var count int64
			if err := db.Model(&model.ClientTrafficResetBatch{}).Where("request_id = ?", original.Operation.RequestID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("rejected program committed SQL operation: %d/%v", count, err)
			}
			if _, err := state.Journal.LookupResetCompletion(capture.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("rejected program gained completion: %v", err)
			}
		})
	}
}
