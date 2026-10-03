package service

import (
	"errors"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Only protected metadata is projected here. Allocation, usage, core windows
// and operation completion belong to the actual execution path.
func recoverAuthorityPreparedRenewalTx(tx *gorm.DB, journal *policyauthority.Journal, source string, capture policyauthority.ResetOperationCapture) error {
	if err := validateAuthorityDirectResetSourceTx(tx, source); err != nil {
		return err
	}
	_, err := decodeAuthorityRenewalCapture(capture, journal, source)
	if err != nil {
		return err
	}
	prepared, err := journal.LookupResetPreparation(capture.RequestID)
	if errors.Is(err, policyauthority.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	snapshot, err := decodeAuthorityRenewalPreparation(prepared, capture, journal, source)
	if err != nil {
		return err
	}
	expiries := make(map[string]int64, len(snapshot.Effects))
	for _, effect := range snapshot.Effects {
		account, err := journal.LookupAccount(effect.ClientID)
		if err != nil {
			return err
		}
		if account.Deleted {
			continue
		}
		var client model.ClientRecord
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&client, "stable_id = ?", effect.ClientID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err := validateLocalClientPolicyResetScope(tx, []string{effect.ClientID}); err != nil {
			return err
		}
		var traffic xray.ClientTraffic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&traffic, "email = ?", client.Email).Error; err != nil {
			return err
		}
		if traffic.ResetCount < effect.BeforeResetCount {
			return ErrClientPolicyLedger
		}
		// A versioned or later-stamped edit remains the desired configuration.
		// Prepared effects already happened under the captured renewal rules.
		// Older desired rules remain SQL's configuration after effect recovery.
		if client.ExpiryTime == effect.BeforeExpiryTime && client.UpdatedAt <= effect.BeforeUpdatedAt && client.DesiredPolicyVersion > effect.BeforePolicyVersion {
			return ErrClientPolicyLedger
		}
		if client.ExpiryTime == effect.BeforeExpiryTime && client.DesiredPolicyVersion <= effect.BeforePolicyVersion && client.UpdatedAt <= effect.BeforeUpdatedAt {
			client.ExpiryTime = effect.AfterExpiryTime
			client.UpdatedAt = max(client.UpdatedAt, effect.AfterUpdatedAt)
			if err := tx.Table("clients").Where("stable_id = ?", effect.ClientID).Updates(map[string]any{"expiry_time": client.ExpiryTime, "updated_at": client.UpdatedAt}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&traffic).Updates(map[string]any{"expiry_time": client.ExpiryTime, "reset_count": max(traffic.ResetCount, effect.AfterResetCount)}).Error; err != nil {
			return err
		}
		expiries[client.Email] = client.ExpiryTime
		if client.DesiredPolicyVersion < effect.AfterPolicyVersion && account.Policy.Version < uint64(effect.AfterPolicyVersion) {
			if err := tx.Table("clients").Where("stable_id = ?", effect.ClientID).Updates(map[string]any{"desired_policy_version": effect.AfterPolicyVersion, "policy_fingerprint": effect.AfterPolicyFingerprint}).Error; err != nil {
				return err
			}
		}
	}
	if err := updateManagedRenewalInbounds(tx, expiries, snapshot.ResetAt); err != nil {
		return err
	}
	return recoverAuthorityPreparedResetRowsTx(tx, journal, source, snapshot.Resets, 0)
}
