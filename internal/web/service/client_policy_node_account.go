package service

import (
	"context"
	"errors"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Account preparation alone grants no authority; enrollment still requires both
// original journals and the actual source/role/boot proof.
func prepareNodeClientPolicyContextForDatabase(ctx context.Context, expected *gorm.DB, parentID, nodeID, sourceID string) (model.ClientPolicyNodeAccount, clientpolicy.Policy, error) {
	var account model.ClientPolicyNodeAccount
	var policy clientpolicy.Policy
	if ctx == nil || expected == nil || !validPolicySourceKey(nodeID) || !validPolicySourceKey(sourceID) {
		return account, policy, ErrClientPolicyLedger
	}
	err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		var parent model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "stable_id = ?", parentID).Error; err != nil {
			return err
		}
		if parent.Policy.EffectiveScope() != model.ClientPolicyScopeNode {
			return clientpolicy.ErrInvalidPolicy
		}
		if err := rejectDeletedClientPolicies(tx, []string{parentID}); err != nil {
			return err
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, "parent_client_id = ? AND (node_id = ? OR source_id = ?)", parentID, nodeID, sourceID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			account = model.ClientPolicyNodeAccount{ParentClientID: parentID, NodeID: nodeID, SourceID: sourceID}
			if err := tx.Create(&account).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if account.NodeID != nodeID || account.SourceID != sourceID {
			return ErrClientPolicyLedger
		}
		if err := rejectDeletedClientPolicies(tx, []string{account.ClientID}); err != nil {
			return err
		}
		resets, err := latestClientPolicyResets(tx, []string{account.ClientID})
		if err != nil {
			return err
		}
		if err := validateLocalClientPolicyResetScope(tx, []string{parentID}); err != nil {
			return err
		}
		parent.StableID, parent.DesiredPolicyVersion, parent.PolicyFingerprint = account.ClientID, account.DesiredPolicyVersion, account.PolicyFingerprint
		var fingerprint string
		policy, fingerprint, err = fingerprintClientPolicy(parent, resets[account.ClientID])
		if err != nil {
			return err
		}
		version, changed, err := nextClientPolicyVersion(account.DesiredPolicyVersion, account.PolicyFingerprint, fingerprint)
		if err != nil {
			return err
		}
		if changed {
			if err := tx.Table("client_policy_node_accounts").Where("client_id = ?", account.ClientID).Updates(map[string]any{"desired_policy_version": version, "policy_fingerprint": fingerprint}).Error; err != nil {
				return err
			}
		}
		account.DesiredPolicyVersion, account.PolicyFingerprint, policy.Version = version, fingerprint, uint64(version)
		return nil
	})
	if err != nil {
		return model.ClientPolicyNodeAccount{}, clientpolicy.Policy{}, err
	}
	return account, policy, nil
}
