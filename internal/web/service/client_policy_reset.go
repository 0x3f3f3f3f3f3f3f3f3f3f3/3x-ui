package service

import (
	"errors"
	"math"

	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func latestClientPolicyResets(tx *gorm.DB, clientIDs []string) (map[string]*model.ClientPolicyReset, error) {
	latest := tx.Model(&model.ClientPolicyReset{}).Select("MAX(id)").Where("client_id IN ?", clientIDs).Group("client_id")
	var rows []model.ClientPolicyReset
	if err := tx.Where("id IN (?)", latest).Find(&rows).Error; err != nil {
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
	var policy clientpolicy.Policy
	if !validPolicySourceKey(instanceID) || !validPolicySourceKey(requestID) || clientID == "" {
		return policy, ErrClientPolicyLedger
	}
	err := runSerializedTx(func(tx *gorm.DB) error {
		var source model.ClientPolicySource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "instance_id = ?", instanceID).Error; err != nil {
			return err
		}
		if source.NodeKey != "local" || source.Epoch <= 0 || source.Sequence <= 0 {
			return ErrClientPolicyLedger
		}
		var client model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&client, "stable_id = ?", clientID).Error; err != nil {
			return err
		}
		resets, err := latestClientPolicyResets(tx, []string{clientID})
		if err != nil {
			return err
		}
		latest := resets[clientID]
		if latest != nil && latest.InstanceID != instanceID {
			return ErrClientPolicyLedger
		}
		if err := validateLocalClientPolicyResetScope(tx, []string{clientID}); err != nil {
			return err
		}
		var receipts []model.ClientPolicyReceipt
		if err := tx.Where("client_id = ?", clientID).Find(&receipts).Error; err != nil {
			return err
		}
		if len(receipts) != 1 {
			return ErrClientPolicyLedger
		}
		receipt := receipts[0]
		if receipt.InstanceID != instanceID || receipt.Epoch > source.Epoch || receipt.Sequence > source.Sequence || receipt.PolicyVersion <= 0 || receipt.PolicyVersion > client.DesiredPolicyVersion || receipt.ReservedBytes < 0 || receipt.Revoked {
			return ErrClientPolicyLedger
		}
		var total model.ClientPolicyTotal
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&total, "client_id = ?", clientID).Error; err != nil {
			return err
		}
		if total.RawUpload != receipt.RawUpload || total.RawDownload != receipt.RawDownload || total.BilledBytes != receipt.BilledBytes || total.UncertainBytes != receipt.UncertainBytes {
			return ErrClientPolicyLedger
		}
		var existing model.ClientPolicyReset
		err = tx.First(&existing, "client_id = ? AND request_id = ?", clientID, requestID).Error
		if err == nil {
			if existing.InstanceID != instanceID {
				return ErrClientPolicyLedger
			}
			policy, err = prepareClientPolicyRecord(tx, client, latest)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		reset := model.ClientPolicyReset{ClientID: clientID, RequestID: requestID, InstanceID: instanceID, Epoch: receipt.Epoch, Sequence: receipt.Sequence, RawUpload: receipt.RawUpload, RawDownload: receipt.RawDownload, BilledBytes: receipt.BilledBytes, Remainder: receipt.Remainder, UncertainBytes: receipt.UncertainBytes}
		policy, err = prepareClientPolicyRecord(tx, client, &reset)
		if err != nil {
			return err
		}
		reset.PolicyVersion = int64(policy.Version)
		return tx.Create(&reset).Error
	})
	if err != nil {
		return clientpolicy.Policy{}, err
	}
	return policy, nil
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
