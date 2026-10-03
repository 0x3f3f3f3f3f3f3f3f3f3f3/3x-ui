package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

// Private source-owned zero-effect programs exercise storage traversal, not
// public client-count or protocol performance acceptance.
func startupZeroEffectProgram(t *testing.T, state *durableAuthorityState, request, scope string, prepare bool) policyauthority.ResetOperationPreparation {
	t.Helper()
	operation := model.ClientTrafficResetBatch{RequestID: request, Scope: scope, TargetsJSON: "[]", InboundIDsJSON: "[]", ManagedIDsJSON: "[]", SelectionHash: "0000000000000000000000000000000000000000000000000000000000000000", CreatedAt: 100, Applied: true}
	raw, err := json.Marshal(authorityResetCaptureSnapshot{Schema: 1, Operation: operation})
	if err != nil {
		t.Fatal(err)
	}
	capture := policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: state.SourceID, RequestID: authorityResetRequestKey(request), Snapshot: string(raw)}
	if err := state.Journal.CaptureResetOperation(capture); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(authorityResetPreparationSnapshot{Schema: 1, RequestID: request, ResetAt: 100, ActiveManagedIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	prepared := policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: string(raw)}
	if prepare {
		if err := state.Journal.PrepareResetOperation(prepared); err != nil {
			t.Fatal(err)
		}
	}
	return prepared
}

// Treating capture-only or opaque compatibility data as an executed program
// would manufacture acknowledgements without a supported original preparation.
func TestManagedAuthorityStartupAcknowledgementKeepsMetadataAndCompatibilityBoundaries(t *testing.T) {
	for _, kind := range []string{"capture-only", "schema1-inbound", "opaque-prepared", "unsupported-prepared"} {
		t.Run(kind, func(t *testing.T) {
			svc, inbound, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			cfg := managedAuthorityForProcess(currentXrayProcess()).config
			if err := svc.StopXray(); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(filepath.Dir(cfg.StateFile), "authority")
			state, err := openAuthorityState(dir)
			if err != nil {
				t.Fatal(err)
			}
			before, err := state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			var prepared policyauthority.ResetOperationPreparation
			if kind == "opaque-prepared" {
				capture := policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: state.SourceID, RequestID: "startup-storage-compatibility", Snapshot: `{"opaque":"capture"}`}
				if err := state.Journal.CaptureResetOperation(capture); err != nil {
					t.Fatal(err)
				}
				prepared = policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: `{"opaque":"prepared"}`}
				if err := state.Journal.PrepareResetOperation(prepared); err != nil {
					t.Fatal(err)
				}
			} else {
				scope := "bulk"
				if kind != "capture-only" {
					scope = fmt.Sprintf("inbound:%d", inbound.Id)
				}
				prepared = startupZeroEffectProgram(t, state, "startup-compatibility-"+kind, scope, kind == "unsupported-prepared")
			}
			if err := state.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			err = recoverAuthorityDesiredState(ctx, &cfg)
			if kind == "unsupported-prepared" {
				if !errors.Is(err, ErrClientPolicyLedger) {
					t.Fatalf("unsupported original effects accepted: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			state, err = openAuthorityState(dir)
			if err != nil {
				t.Fatal(err)
			}
			after, err := state.Journal.Account(client.StableID)
			if err != nil || after != before {
				t.Fatalf("metadata recovery changed account: %v", err)
			}
			if _, err := state.Journal.LookupResetCompletion(prepared.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("metadata invented acknowledgement: %v", err)
			}
			if err := state.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			err = svc.RestartXray(true)
			if kind == "unsupported-prepared" {
				if !errors.Is(err, ErrClientPolicyLedger) || currentXrayProcess().IsRunning() {
					t.Fatalf("unsupported startup opened service: %v", err)
				}
				state, err = openAuthorityState(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer state.Journal.Close()
			} else {
				if err != nil {
					t.Fatal(err)
				}
				state = managedAuthorityForProcess(currentXrayProcess()).state
			}
			if _, err := state.Journal.LookupResetCompletion(prepared.RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("compatibility data gained completion: %v", err)
			}
			if kind == "opaque-prepared" || kind == "unsupported-prepared" {
				retained, err := state.Journal.LookupResetPreparation(prepared.RequestID)
				if err != nil || retained != prepared {
					t.Fatalf("compatibility preparation changed: %v", err)
				}
			}
		})
	}
}
