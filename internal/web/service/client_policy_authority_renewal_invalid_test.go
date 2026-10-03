package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func TestManagedAuthorityRenewalRecoveryRejectsInvalidPreparation(t *testing.T) {
	for _, kind := range []string{"wrong-membership", "usage-above-account", "wrong-semantic-request", "unknown-fields"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAuthorityRenewalRecoveryFixture(t, false)
			restoreAuthorityRenewalSQL(t, f)
			state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			before, err := state.Journal.Account(f.client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			original, err := decodeAuthorityRenewalCapture(f.capture, state.Journal, state.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			original.Triggers[0].ResetMax = 3
			raw, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			capture := policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: state.SourceID, RequestID: authorityRenewalKey(state.SourceID, original.Zone, original.Triggers), Snapshot: string(raw)}
			if err := state.Journal.CaptureResetOperation(capture); err != nil {
				t.Fatal(err)
			}
			snapshot := f.snapshot
			snapshot.RequestID = capture.RequestID
			snapshot.Resets[0].RequestID = authorityRenewalClientRequest(state.SourceID, original.Zone, original.Triggers[0])
			switch kind {
			case "wrong-membership":
				snapshot.Effects[0].ClientID = uuid.NewString()
			case "usage-above-account":
				snapshot.Resets[0].RawUpload = int64(before.Usage.RawUpload) + 1
				snapshot.Resets[0].BilledBytes = int64(before.Usage.BilledBytes) + 1
			case "wrong-semantic-request":
				snapshot.Resets[0].RequestID = "renewal:other-original-trigger"
			}
			raw, err = json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "unknown-fields" {
				raw = append(raw[:len(raw)-1], []byte(`,"Unknown":true}`)...)
			}
			prepared := policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: string(raw)}
			if err := state.Journal.PrepareResetOperation(prepared); err != nil {
				t.Fatal(err)
			}
			err = recoverAuthorityResetCaptures(context.Background(), database.GetDB(), state.Journal, state.SourceID)
			if !errors.Is(err, ErrClientPolicyLedger) {
				t.Fatalf("invalid renewal %s accepted: %v", kind, err)
			}
			after, err := state.Journal.Account(f.client.StableID)
			if err != nil || before != after {
				t.Fatalf("invalid evidence changed complete authority account: %v", err)
			}
			if _, err := state.Journal.LookupResetCompletion(capture.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("invalid renewal metadata was completed: %v", err)
			}
		})
	}
}
