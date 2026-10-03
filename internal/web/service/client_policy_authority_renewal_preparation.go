package service

import (
	"cmp"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func renewalTrigger(client model.ClientRecord, count int) authorityRenewalTrigger {
	return authorityRenewalTrigger{ClientID: client.StableID, ExpiryTime: client.ExpiryTime, ResetCount: count, Reset: client.Reset, ResetDay: client.ResetDay, ResetWeekday: client.ResetWeekday, ResetMax: client.ResetMax}
}

// Preparation fixes the original effects before the SQL transaction commits.
// A prepared retry restores that boundary and compiles today's desired fields.
func prepareAuthorityClientRenewalsTx(tx *gorm.DB, state *durableAuthorityState, capture policyauthority.ResetOperationCapture, active []string) ([]clientpolicy.Policy, error) {
	if err := validateAuthorityDirectResetSourceTx(tx, state.SourceID); err != nil {
		return nil, err
	}
	original, err := decodeAuthorityRenewalCapture(capture, state.Journal, state.SourceID)
	if err != nil {
		return nil, err
	}
	if _, err := state.Journal.LookupResetPreparation(capture.RequestID); err == nil {
		if err := recoverAuthorityPreparedRenewalTx(tx, state.Journal, state.SourceID, capture); err != nil {
			return nil, err
		}
		return prepareCurrentRenewalPoliciesTx(tx, active)
	} else if !errors.Is(err, policyauthority.ErrNotFound) {
		return nil, err
	}
	location, err := time.LoadLocation(original.Zone)
	if err != nil {
		return nil, err
	}
	now := max(time.Now().UnixMilli(), original.At)
	triggers := make(map[string]authorityRenewalTrigger, len(original.Triggers))
	for _, trigger := range original.Triggers {
		triggers[trigger.ClientID] = trigger
	}
	var clients []model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", active).Order("stable_id").Find(&clients).Error; err != nil {
		return nil, err
	}
	before := make(map[string]model.ClientRecord, len(clients))
	var eligible []string
	requests := make(map[string]string, len(clients))
	for _, client := range clients {
		var traffic xray.ClientTraffic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&traffic, "email = ?", client.Email).Error; err != nil {
			return nil, err
		}
		trigger, captured := triggers[client.StableID]
		if !captured || renewalTrigger(client, traffic.ResetCount) != trigger {
			continue
		}
		before[client.StableID] = client
		eligible = append(eligible, client.StableID)
		requests[client.StableID] = authorityRenewalClientRequest(state.SourceID, original.Zone, trigger)
	}
	policies, err := prepareClientPolicyRenewalsWithRequests(tx, state.SourceID, eligible, now, location, requests)
	if err != nil {
		return nil, err
	}
	snapshot := authorityRenewalPreparationSnapshot{Schema: 1, RequestID: capture.RequestID, ResetAt: now}
	selected := make(map[string]bool, len(policies))
	var resetRequests []string
	for _, policy := range policies {
		var after model.ClientRecord
		if err := tx.First(&after, "stable_id = ?", policy.ClientID).Error; err != nil {
			return nil, err
		}
		var traffic xray.ClientTraffic
		if err := tx.First(&traffic, "email = ?", after.Email).Error; err != nil {
			return nil, err
		}
		prior := before[policy.ClientID]
		snapshot.Effects = append(snapshot.Effects, authorityRenewalEffect{ClientID: policy.ClientID,
			BeforeExpiryTime: prior.ExpiryTime, AfterExpiryTime: after.ExpiryTime,
			BeforeResetCount: triggers[policy.ClientID].ResetCount, AfterResetCount: traffic.ResetCount,
			BeforeUpdatedAt: prior.UpdatedAt, AfterUpdatedAt: after.UpdatedAt,
			BeforePolicyVersion: prior.DesiredPolicyVersion, AfterPolicyVersion: after.DesiredPolicyVersion,
			BeforePolicyFingerprint: prior.PolicyFingerprint, AfterPolicyFingerprint: after.PolicyFingerprint})
		selected[policy.ClientID] = true
		resetRequests = append(resetRequests, requests[policy.ClientID])
	}
	if len(resetRequests) > 0 {
		if err := tx.Where("client_id IN ? AND request_id IN ? AND instance_id = ?", eligible, resetRequests, state.SourceID).
			Order("client_id").Find(&snapshot.Resets).Error; err != nil {
			return nil, err
		}
		for i := range snapshot.Resets {
			snapshot.Resets[i].Id = 0
		}
	}
	for _, trigger := range original.Triggers {
		if !selected[trigger.ClientID] {
			snapshot.OmittedIDs = append(snapshot.OmittedIDs, trigger.ClientID)
		}
	}
	slices.SortFunc(snapshot.Effects, func(a, b authorityRenewalEffect) int { return cmp.Compare(a.ClientID, b.ClientID) })
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	prepared := policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: string(raw)}
	if _, err := decodeAuthorityRenewalPreparation(prepared, capture, state.Journal, state.SourceID); err != nil {
		return nil, err
	}
	if err := state.Journal.PrepareResetOperation(prepared); err != nil {
		return nil, err
	}
	return policies, nil
}

func prepareCurrentRenewalPoliciesTx(tx *gorm.DB, ids []string) ([]clientpolicy.Policy, error) {
	if err := validateLocalClientPolicyResetScope(tx, ids); err != nil {
		return nil, err
	}
	var clients []model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", ids).Order("stable_id").Find(&clients).Error; err != nil {
		return nil, err
	}
	latest, err := latestClientPolicyResets(tx, ids)
	if err != nil {
		return nil, err
	}
	var policies []clientpolicy.Policy
	for _, client := range clients {
		policy, err := prepareClientPolicyRecord(tx, client, latest[client.StableID])
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, nil
}
