package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Versions describe enforcement fields, not account names or credentials; retries reuse the committed version.
func PrepareClientPolicies(clientIDs []string) ([]clientpolicy.Policy, error) {
	if len(clientIDs) == 0 || len(clientIDs) > 1000 {
		return nil, clientpolicy.ErrInvalidPolicy
	}
	seen := make(map[string]bool, len(clientIDs))
	for _, id := range clientIDs {
		if id == "" || seen[id] {
			return nil, clientpolicy.ErrInvalidPolicy
		}
		seen[id] = true
	}
	var policies []clientpolicy.Policy
	err := runSerializedTx(func(tx *gorm.DB) error {
		var clients []model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", clientIDs).Order("stable_id").Find(&clients).Error; err != nil {
			return err
		}
		if len(clients) != len(clientIDs) {
			return gorm.ErrRecordNotFound
		}
		for _, client := range clients {
			policy, err := desiredClientPolicy(client)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(policy)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(raw)
			fingerprint := hex.EncodeToString(hash[:])
			version := client.DesiredPolicyVersion
			if version < 0 || version == 0 && client.PolicyFingerprint != "" || version > 0 && client.PolicyFingerprint == "" {
				return ErrClientPolicyLedger
			}
			if client.PolicyFingerprint != fingerprint {
				if version == math.MaxInt64 {
					return clientpolicy.ErrOverflow
				}
				version++
				// These columns are create-only in ordinary ORM saves; only reconciliation advances them.
				if err := tx.Table("clients").Where("id = ?", client.Id).Updates(map[string]any{
					"desired_policy_version": version,
					"policy_fingerprint":     fingerprint,
				}).Error; err != nil {
					return err
				}
			}
			policy.Version = uint64(version)
			policies = append(policies, policy)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return policies, nil
}

func desiredClientPolicy(client model.ClientRecord) (clientpolicy.Policy, error) {
	if err := client.Policy.Validate(); err != nil {
		return clientpolicy.Policy{}, err
	}
	if client.TotalGB < 0 || client.ExpiryTime < 0 {
		return clientpolicy.Policy{}, fmt.Errorf("%w: managed quota must be nonnegative and first-use expiry requires activation support", clientpolicy.ErrInvalidPolicy)
	}
	multiplier, err := client.Policy.MultiplierMicros()
	if err != nil {
		return clientpolicy.Policy{}, err
	}
	policy := clientpolicy.Policy{
		ClientID: client.StableID, Version: 1, Enabled: client.Enable,
		Multiplier: multiplier, QuotaBytes: uint64(client.TotalGB),
		ExpiresAt: client.ExpiryTime, BurstBytes: 65536,
	}
	if client.Policy != nil {
		policy.UploadRate = uint64(client.Policy.UploadBytesPerSecond)
		policy.DownloadRate = uint64(client.Policy.DownloadBytesPerSecond)
	}
	return policy, policy.Validate()
}
