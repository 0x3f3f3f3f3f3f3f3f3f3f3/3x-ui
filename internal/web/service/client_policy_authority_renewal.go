package service

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

const authorityRenewalPrefix = "policy-renewal:"

type authorityRenewalTrigger struct {
	ClientID     string
	ExpiryTime   int64
	ResetCount   int
	Reset        int
	ResetDay     int
	ResetWeekday int
	ResetMax     int
}

type authorityRenewalCaptureSnapshot struct {
	Schema   int
	At       int64
	Zone     string
	Triggers []authorityRenewalTrigger
}

type authorityRenewalEffect struct {
	ClientID                string
	BeforeExpiryTime        int64
	AfterExpiryTime         int64
	BeforeResetCount        int
	AfterResetCount         int
	BeforeUpdatedAt         int64
	AfterUpdatedAt          int64
	BeforePolicyVersion     int64
	AfterPolicyVersion      int64
	BeforePolicyFingerprint string
	AfterPolicyFingerprint  string
}

type authorityRenewalPreparationSnapshot struct {
	Schema     int
	RequestID  string
	ResetAt    int64
	Effects    []authorityRenewalEffect
	OmittedIDs []string
	Resets     []model.ClientPolicyReset
}

func authorityRenewalKey(source, zone string, triggers []authorityRenewalTrigger) string {
	raw, _ := json.Marshal([]any{source, zone, triggers})
	return authorityRenewalPrefix + authorityResetSnapshotDigest(string(raw))
}

func authorityRenewalClientRequest(source, zone string, trigger authorityRenewalTrigger) string {
	raw, _ := json.Marshal([]any{source, zone, trigger})
	return "renewal:" + authorityResetSnapshotDigest(string(raw))
}

func validAuthorityRenewalTrigger(trigger authorityRenewalTrigger) bool {
	return uuid.Validate(trigger.ClientID) == nil && trigger.ExpiryTime > 0 && trigger.ResetCount >= 0 && trigger.Reset >= 0 &&
		trigger.ResetDay >= 0 && trigger.ResetDay <= 31 && trigger.ResetWeekday >= 0 && trigger.ResetWeekday <= 7 &&
		(trigger.Reset > 0 || trigger.ResetDay > 0 || trigger.ResetWeekday > 0) && (trigger.ResetMax <= 0 || trigger.ResetCount < trigger.ResetMax)
}

func authorityRenewalIDs(snapshot authorityRenewalCaptureSnapshot) []string {
	ids := make([]string, len(snapshot.Triggers))
	for i, trigger := range snapshot.Triggers {
		ids[i] = trigger.ClientID
	}
	return ids
}

func decodeAuthorityRenewalCapture(capture policyauthority.ResetOperationCapture, journal *policyauthority.Journal, source string) (authorityRenewalCaptureSnapshot, error) {
	var snapshot authorityRenewalCaptureSnapshot
	if journal == nil || capture.Identity != journal.Identity() || capture.SourceID != source || capture.CalendarKey != "" {
		return snapshot, ErrClientPolicyLedger
	}
	decoder := json.NewDecoder(strings.NewReader(capture.Snapshot))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || snapshot.Schema != 1 || snapshot.At <= 0 || len(snapshot.Triggers) == 0 || len(snapshot.Triggers) > 1000 {
		return snapshot, ErrClientPolicyLedger
	}
	if _, err := time.LoadLocation(snapshot.Zone); err != nil {
		return snapshot, ErrClientPolicyLedger
	}
	for i, trigger := range snapshot.Triggers {
		if !validAuthorityRenewalTrigger(trigger) || trigger.ExpiryTime > snapshot.At || i > 0 && trigger.ClientID <= snapshot.Triggers[i-1].ClientID {
			return snapshot, ErrClientPolicyLedger
		}
	}
	if capture.RequestID != authorityRenewalKey(source, snapshot.Zone, snapshot.Triggers) {
		return snapshot, ErrClientPolicyLedger
	}
	return snapshot, nil
}

func validAuthorityRenewalFingerprint(raw string) bool {
	decoded, err := hex.DecodeString(raw)
	return err == nil && len(decoded) == 32 && raw == strings.ToLower(raw)
}

func decodeAuthorityRenewalPreparation(prepared policyauthority.ResetOperationPreparation, capture policyauthority.ResetOperationCapture, journal *policyauthority.Journal, source string) (authorityRenewalPreparationSnapshot, error) {
	var snapshot authorityRenewalPreparationSnapshot
	original, err := decodeAuthorityRenewalCapture(capture, journal, source)
	if err != nil {
		return snapshot, err
	}
	if prepared.Identity != capture.Identity || prepared.SourceID != source || prepared.RequestID != capture.RequestID || prepared.CaptureDigest != authorityResetSnapshotDigest(capture.Snapshot) {
		return snapshot, ErrClientPolicyLedger
	}
	decoder := json.NewDecoder(strings.NewReader(prepared.Snapshot))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || snapshot.Schema != 1 || snapshot.RequestID != capture.RequestID || snapshot.ResetAt < original.At || len(snapshot.Effects)+len(snapshot.OmittedIDs) != len(original.Triggers) || len(snapshot.Resets) > len(snapshot.Effects) {
		return snapshot, ErrClientPolicyLedger
	}
	ids := authorityRenewalIDs(original)
	seen := make(map[string]bool, len(ids))
	triggers := make(map[string]authorityRenewalTrigger, len(ids))
	for _, trigger := range original.Triggers {
		triggers[trigger.ClientID] = trigger
	}
	for i, effect := range snapshot.Effects {
		trigger, ok := triggers[effect.ClientID]
		if !ok || seen[effect.ClientID] || i > 0 && effect.ClientID <= snapshot.Effects[i-1].ClientID || effect.BeforeExpiryTime != trigger.ExpiryTime || effect.BeforeResetCount != trigger.ResetCount || effect.AfterExpiryTime <= effect.BeforeExpiryTime || effect.AfterResetCount <= effect.BeforeResetCount || effect.BeforeUpdatedAt < 0 || effect.AfterUpdatedAt < max(effect.BeforeUpdatedAt, snapshot.ResetAt) || effect.BeforePolicyVersion < 1 || effect.AfterPolicyVersion <= effect.BeforePolicyVersion || !validAuthorityRenewalFingerprint(effect.BeforePolicyFingerprint) || !validAuthorityRenewalFingerprint(effect.AfterPolicyFingerprint) {
			return snapshot, ErrClientPolicyLedger
		}
		if trigger.ResetMax > 0 && effect.AfterResetCount > trigger.ResetMax {
			return snapshot, ErrClientPolicyLedger
		}
		seen[effect.ClientID] = true
	}
	for i, id := range snapshot.OmittedIDs {
		if _, ok := slices.BinarySearch(ids, id); !ok || seen[id] || i > 0 && id <= snapshot.OmittedIDs[i-1] {
			return snapshot, ErrClientPolicyLedger
		}
		seen[id] = true
	}
	resetByID := make(map[string]model.ClientPolicyReset, len(snapshot.Resets))
	for i, reset := range snapshot.Resets {
		trigger, ok := triggers[reset.ClientID]
		if !ok || i > 0 && reset.ClientID <= snapshot.Resets[i-1].ClientID || validateClientPolicyReset(&reset) != nil || reset.Id != 0 || reset.InstanceID != source || reset.RequestID != authorityRenewalClientRequest(source, original.Zone, trigger) || reset.CreatedAt != snapshot.ResetAt {
			return snapshot, ErrClientPolicyLedger
		}
		resetByID[reset.ClientID] = reset
	}
	for _, effect := range snapshot.Effects {
		reset, ok := resetByID[effect.ClientID]
		if ok != (effect.AfterExpiryTime > snapshot.ResetAt) || ok && reset.PolicyVersion != effect.AfterPolicyVersion {
			return snapshot, ErrClientPolicyLedger
		}
		delete(resetByID, effect.ClientID)
	}
	if len(resetByID) != 0 {
		return snapshot, ErrClientPolicyLedger
	}
	return snapshot, nil
}
