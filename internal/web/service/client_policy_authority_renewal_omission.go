package service

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const maxAuthorityRenewalGeneration = 100000

func validAuthorityRenewalGeneration(snapshot authorityRenewalCaptureSnapshot) bool {
	return snapshot.Schema == 1 && snapshot.Generation == 0 || snapshot.Schema == 2 && snapshot.Generation > 0 && snapshot.Generation <= maxAuthorityRenewalGeneration
}

func authorityRenewalGenerationKey(source, zone string, triggers []authorityRenewalTrigger, generation int) string {
	if generation == 0 {
		return authorityRenewalKey(source, zone, triggers)
	}
	raw, _ := json.Marshal([]any{source, zone, triggers, generation})
	return authorityRenewalPrefix + authorityResetSnapshotDigest(string(raw))
}

// Check one predecessor without recursive history decoding. Recovery visits
// every retained capture before projection, so every link is checked once.
func validateAuthorityRenewalPredecessor(journal *policyauthority.Journal, source string, snapshot authorityRenewalCaptureSnapshot) error {
	key := authorityRenewalGenerationKey(source, snapshot.Zone, snapshot.Triggers, snapshot.Generation-1)
	capture, err := journal.LookupResetOperation(key)
	if err != nil {
		return err
	}
	previous, err := decodeAuthorityRenewalCaptureEnvelope(capture, journal, source)
	if err != nil || previous.Generation != snapshot.Generation-1 || previous.At > snapshot.At || previous.Zone != snapshot.Zone || !slices.Equal(previous.Triggers, snapshot.Triggers) {
		return ErrClientPolicyLedger
	}
	return completedAuthorityRenewalOmission(journal, source, capture, previous)
}

func completedAuthorityRenewalOmission(journal *policyauthority.Journal, source string, capture policyauthority.ResetOperationCapture, original authorityRenewalCaptureSnapshot) error {
	prepared, err := journal.LookupResetPreparation(capture.RequestID)
	if err != nil {
		return err
	}
	snapshot, err := decodeAuthorityRenewalPreparationEnvelope(prepared, capture, original, source)
	if err != nil || len(snapshot.Effects) != 0 {
		return ErrClientPolicyLedger
	}
	completed, err := journal.LookupResetCompletion(capture.RequestID)
	if err != nil {
		return err
	}
	if completed.Identity != capture.Identity || completed.SourceID != source || completed.RequestID != capture.RequestID || completed.PreparationDigest != authorityResetSnapshotDigest(prepared.Snapshot) {
		return ErrClientPolicyLedger
	}
	return nil
}

// A later selection can revisit members that never acquired an effect. A
// prepared effect remains immutable even when disposable SQL has gone back.
func nextAuthorityRenewalSelection(journal *policyauthority.Journal, source string, capture policyauthority.ResetOperationCapture, original authorityRenewalCaptureSnapshot, now int64, location *time.Location) (bool, error) {
	prepared, err := journal.LookupResetPreparation(capture.RequestID)
	if errors.Is(err, policyauthority.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	snapshot, err := decodeAuthorityRenewalPreparationEnvelope(prepared, capture, original, source)
	if err != nil || len(snapshot.Effects) != 0 {
		return false, err
	}
	for _, trigger := range original.Triggers {
		traffic := xray.ClientTraffic{ExpiryTime: trigger.ExpiryTime, ResetCount: trigger.ResetCount, Reset: trigger.Reset, ResetDay: trigger.ResetDay, ResetWeekday: trigger.ResetWeekday, ResetMax: trigger.ResetMax}
		if _, count := catchUpClientRenewal(&traffic, now, location); count > 0 {
			if err := completedAuthorityRenewalOmission(journal, source, capture, original); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}
