package service

import (
	"math"
	"slices"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func latestClientPolicyResets(tx *gorm.DB, clientIDs []string) (map[string]*model.ClientPolicyReset, error) {
	var rows []model.ClientPolicyReset
	if err := tx.Where(`client_id IN ? AND NOT EXISTS (
 SELECT 1 FROM client_policy_resets newer
 WHERE newer.client_id = client_policy_resets.client_id
 AND (newer.policy_version > client_policy_resets.policy_version
 OR (newer.policy_version = client_policy_resets.policy_version AND newer.id > client_policy_resets.id)))`, clientIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]*model.ClientPolicyReset, len(rows))
	for i := range rows {
		if rows[i].PolicyVersion <= 0 {
			return nil, ErrClientPolicyLedger
		}
		result[rows[i].ClientID] = &rows[i]
	}
	return result, nil
}

func validateClientPolicyReset(reset *model.ClientPolicyReset) error {
	if !validPolicySourceKey(reset.InstanceID) || !validPolicySourceKey(reset.RequestID) || reset.Epoch <= 0 || reset.Sequence <= 0 || reset.RawUpload < 0 || reset.RawDownload < 0 || reset.BilledBytes < 0 || reset.UncertainBytes < 0 || reset.Remainder < 0 || reset.Remainder >= int64(clientpolicy.MultiplierScale) || reset.BilledBytes > math.MaxInt64-reset.UncertainBytes {
		return ErrClientPolicyLedger
	}
	return nil
}

// The boundary is a committed receipt; outstanding reservations are never credited as spent usage.
func PrepareClientPolicyReset(instanceID, clientID, requestID string) (clientpolicy.Policy, error) {
	policies, err := PrepareClientPolicyResets(instanceID, []string{clientID}, requestID)
	if err != nil {
		return clientpolicy.Policy{}, err
	}
	return policies[0], nil
}

func PrepareClientPolicyResets(instanceID string, clientIDs []string, requestID string) ([]clientpolicy.Policy, error) {
	if !validPolicySourceKey(instanceID) || !validPolicySourceKey(requestID) {
		return nil, ErrClientPolicyLedger
	}
	ids, err := clientPolicyResetIDs(clientIDs)
	if err != nil {
		return nil, err
	}
	var policies []clientpolicy.Policy
	err = runSerializedTx(func(tx *gorm.DB) error {
		var err error
		policies, err = prepareClientPolicyResetsTx(tx, instanceID, ids, requestID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return policies, nil
}

func prepareClientPolicyResetsTx(tx *gorm.DB, instanceID string, ids []string, requestID string) ([]clientpolicy.Policy, error) {
	return prepareClientPolicyResetsAtTx(tx, instanceID, ids, requestID, time.Now().UnixMilli())
}

func prepareClientPolicyResetsAtTx(tx *gorm.DB, instanceID string, ids []string, requestID string, at int64) ([]clientpolicy.Policy, error) {
	var source model.ClientPolicySource
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "instance_id = ?", instanceID).Error; err != nil {
		return nil, err
	}
	if source.NodeKey != "local" || source.Epoch <= 0 || source.Sequence <= 0 {
		return nil, ErrClientPolicyLedger
	}
	var policies []clientpolicy.Policy
	for _, batch := range chunkStrings(ids, 1000) {
		prepared, err := prepareClientPolicyResetBatch(tx, source, batch, requestID, at)
		if err != nil {
			return nil, err
		}
		policies = append(policies, prepared...)
	}
	return policies, nil
}

func clientPolicyResetIDs(clientIDs []string) ([]string, error) {
	if len(clientIDs) == 0 || len(clientIDs) > 100000 {
		return nil, ErrClientPolicyLedger
	}
	ids := slices.Clone(clientIDs)
	slices.Sort(ids)
	for i, id := range ids {
		if id == "" || i > 0 && ids[i-1] == id {
			return nil, ErrClientPolicyLedger
		}
	}
	return ids, nil
}

func prepareClientPolicyResetBatch(tx *gorm.DB, source model.ClientPolicySource, ids []string, requestID string, at int64) ([]clientpolicy.Policy, error) {
	var clients []model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", ids).Order("stable_id").Find(&clients).Error; err != nil {
		return nil, err
	}
	if len(clients) != len(ids) {
		return nil, gorm.ErrRecordNotFound
	}
	latest, err := latestClientPolicyResets(tx, ids)
	if err != nil {
		return nil, err
	}
	if err := validateLocalClientPolicyResetScope(tx, ids); err != nil {
		return nil, err
	}
	var receipts []model.ClientPolicyReceipt
	if err := tx.Where("client_id IN ?", ids).Find(&receipts).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]model.ClientPolicyReceipt, len(receipts))
	for _, receipt := range receipts {
		if _, exists := byID[receipt.ClientID]; exists {
			return nil, ErrClientPolicyLedger
		}
		byID[receipt.ClientID] = receipt
	}
	var totals []model.ClientPolicyTotal
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("client_id IN ?", ids).Order("client_id").Find(&totals).Error; err != nil {
		return nil, err
	}
	if len(totals) != len(ids) {
		return nil, ErrClientPolicyLedger
	}
	var existing []model.ClientPolicyReset
	if err := tx.Where("client_id IN ? AND request_id = ?", ids, requestID).Find(&existing).Error; err != nil {
		return nil, err
	}
	retries := make(map[string]bool, len(existing))
	for _, reset := range existing {
		if reset.InstanceID != source.InstanceID {
			return nil, ErrClientPolicyLedger
		}
		retries[reset.ClientID] = true
	}
	policies := make([]clientpolicy.Policy, 0, len(clients))
	var resetIDs []string
	for i, client := range clients {
		receipt, ok := byID[client.StableID]
		if !ok || receipt.InstanceID != source.InstanceID || receipt.Epoch > source.Epoch || receipt.Sequence > source.Sequence || receipt.PolicyVersion <= 0 || receipt.PolicyVersion > client.DesiredPolicyVersion || receipt.ReservedBytes < 0 || receipt.Revoked {
			return nil, ErrClientPolicyLedger
		}
		total := totals[i]
		if total.ClientID != client.StableID || total.RawUpload != receipt.RawUpload || total.RawDownload != receipt.RawDownload || total.BilledBytes != receipt.BilledBytes || total.UncertainBytes != receipt.UncertainBytes {
			return nil, ErrClientPolicyLedger
		}
		reset := latest[client.StableID]
		if reset != nil && reset.InstanceID != source.InstanceID {
			return nil, ErrClientPolicyLedger
		}
		if !retries[client.StableID] {
			reset = &model.ClientPolicyReset{ClientID: client.StableID, RequestID: requestID, InstanceID: source.InstanceID, Epoch: receipt.Epoch, Sequence: receipt.Sequence, RawUpload: receipt.RawUpload, RawDownload: receipt.RawDownload, BilledBytes: receipt.BilledBytes, Remainder: receipt.Remainder, UncertainBytes: receipt.UncertainBytes, CreatedAt: at}
		}
		policy, err := prepareClientPolicyRecord(tx, client, reset)
		if err != nil {
			return nil, err
		}
		if !retries[client.StableID] {
			// Window identity changes even when no traffic has arrived since the
			// previous reset. Fingerprint equality must not reuse its version.
			if policy.Version == uint64(client.DesiredPolicyVersion) {
				if client.DesiredPolicyVersion == math.MaxInt64 {
					return nil, clientpolicy.ErrOverflow
				}
				policy.Version++
				if err := tx.Table("clients").Where("id = ?", client.Id).Update("desired_policy_version", int64(policy.Version)).Error; err != nil {
					return nil, err
				}
			}
			reset.PolicyVersion = int64(policy.Version)
			if err := tx.Create(reset).Error; err != nil {
				return nil, err
			}
			resetIDs = append(resetIDs, client.StableID)
		}
		policies = append(policies, policy)
	}
	return policies, recordClientTrafficResetTimes(tx, resetIDs, at)
}

func validateLocalClientPolicyResetScope(tx *gorm.DB, clientIDs []string) error {
	var remote int64
	if err := tx.Table("client_inbounds ci").Joins("JOIN clients c ON c.id = ci.client_id").Joins("JOIN inbounds i ON i.id = ci.inbound_id").Where("c.stable_id IN ? AND i.node_id IS NOT NULL", clientIDs).Count(&remote).Error; err != nil {
		return err
	}
	if remote != 0 {
		return ErrClientPolicyLedger
	}
	return nil
}
