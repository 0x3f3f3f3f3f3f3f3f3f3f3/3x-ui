package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Credentials never enter the independent authority journal.
type authorityPolicyEvidence struct {
	Schema   int                      `json:"schema"`
	SourceID string                   `json:"sourceId"`
	Policy   clientpolicy.Policy      `json:"policy"`
	Reset    *model.ClientPolicyReset `json:"reset,omitempty"`
}

type authorityAccountProjection struct {
	policyauthority.Account
	ProtectedReset *model.ClientPolicyReset `json:"protectedReset,omitempty"`
}

func authorityProtectedReset(journal *policyauthority.Journal, account policyauthority.Account) (*model.ClientPolicyReset, error) {
	if account.Policy == account.Seed.Policy {
		after := ""
		for {
			page, err := journal.MigrationPage("resets", account.Seed.ClientID+"/", after, 128)
			if err != nil {
				return nil, err
			}
			if len(page) == 0 {
				return nil, nil
			}
			for _, record := range page {
				var reset model.ClientPolicyReset
				if json.Unmarshal(record.Value, &reset) != nil || validateClientPolicyReset(&reset) != nil || reset.ClientID != account.Seed.ClientID || reset.PolicyVersion <= 0 || uint64(reset.PolicyVersion) > account.Policy.Version {
					return nil, ErrClientPolicyLedger
				}
				digest := sha256.Sum256([]byte(reset.ClientID + "/" + reset.RequestID))
				if account.Policy.WindowID == "reset:"+hex.EncodeToString(digest[:]) {
					return &reset, nil
				}
			}
			after = page[len(page)-1].Key
		}
	}
	change, err := journal.LookupChange(account.Seed.ClientID, fmt.Sprintf("desired:%d", account.Policy.Version))
	if errors.Is(err, policyauthority.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if change.Request.Evidence == "" {
		return nil, nil
	}
	var proof authorityPolicyEvidence
	if json.Unmarshal([]byte(change.Request.Evidence), &proof) != nil {
		return nil, ErrClientPolicyLedger
	}
	evidence, err := decodeAuthorityEvidence(change, proof.SourceID)
	if err != nil {
		return nil, err
	}
	return evidence.Reset, nil
}

func protectedAccountingReset(row clientPolicyAccountingRow, account *policyauthority.Account) bool {
	if account == nil {
		return false
	}
	var projection authorityAccountProjection
	if json.Unmarshal([]byte(row.Authority.AccountJSON), &projection) != nil || projection.ProtectedReset == nil {
		return false
	}
	reset := *projection.ProtectedReset
	reset.Id = row.Reset.Id
	if reset != row.Reset || reset.PolicyVersion <= 0 || uint64(reset.PolicyVersion) > account.Policy.Version || reset.InstanceID != row.InstanceID {
		return false
	}
	digest := sha256.Sum256([]byte(reset.ClientID + "/" + reset.RequestID))
	whole, remainder := account.WindowBaseline, account.WindowBaselineRemainder
	if remainder < account.WindowBaseRemainder {
		if whole == 0 {
			return false
		}
		whole--
		remainder += clientpolicy.MultiplierScale
	}
	if whole < account.WindowBaseUsed {
		return false
	}
	whole -= account.WindowBaseUsed
	remainder -= account.WindowBaseRemainder
	return account.Policy.WindowID == "reset:"+hex.EncodeToString(digest[:]) && whole == uint64(reset.BilledBytes) && remainder == uint64(reset.Remainder)
}

func authorityEvidenceTx(tx *gorm.DB, source string, policy clientpolicy.Policy) (string, error) {
	resets, err := latestClientPolicyResets(tx, []string{policy.ClientID})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(authorityPolicyEvidence{Schema: 1, SourceID: source, Policy: policy, Reset: resets[policy.ClientID]})
	return string(encoded), err
}

func decodeAuthorityEvidence(change policyauthority.Change, source string) (*authorityPolicyEvidence, error) {
	var evidence authorityPolicyEvidence
	if change.Request.Evidence == "" || json.Unmarshal([]byte(change.Request.Evidence), &evidence) != nil || evidence.Schema != 1 || !validPolicySourceKey(source) || evidence.SourceID != source || evidence.Policy.Validate() != nil || evidence.Policy.ClientID != change.Request.ClientID || authorityPolicy(evidence.Policy, change.Request.Policy.WindowID) != change.Request.Policy {
		return nil, ErrClientPolicyLedger
	}
	window := "initial:" + evidence.Policy.ClientID
	if reset := evidence.Reset; reset != nil {
		if validateClientPolicyReset(reset) != nil || reset.Id <= 0 || reset.ClientID != evidence.Policy.ClientID || reset.InstanceID != source || reset.PolicyVersion <= 0 || uint64(reset.PolicyVersion) > evidence.Policy.Version || evidence.Policy.QuotaBaselineBytes != uint64(reset.BilledBytes+reset.UncertainBytes) || evidence.Policy.QuotaBaselineRemainder != uint64(reset.Remainder) {
			return nil, ErrClientPolicyLedger
		}
		digest := sha256.Sum256([]byte(reset.ClientID + "/" + reset.RequestID))
		window = "reset:" + hex.EncodeToString(digest[:])
	} else if evidence.Policy.QuotaBaselineBytes != 0 || evidence.Policy.QuotaBaselineRemainder != 0 {
		return nil, ErrClientPolicyLedger
	}
	if window != change.Request.Policy.WindowID {
		return nil, ErrClientPolicyLedger
	}
	if change.Request.Reset {
		if evidence.Reset == nil {
			return nil, ErrClientPolicyLedger
		}
		baseline := change.UsageBoundary // Earlier journal writers reset at commit time.
		if change.Request.HasResetBaseline {
			baseline = change.Request.ResetBaseline
		}
		reset := evidence.Reset
		if baseline != (policyauthority.Usage{RawUpload: uint64(reset.RawUpload), RawDownload: uint64(reset.RawDownload), BilledBytes: uint64(reset.BilledBytes), Remainder: uint64(reset.Remainder)}) {
			return nil, ErrClientPolicyLedger
		}
	}
	return &evidence, nil
}

// Called under the lifecycle lock before compiling a cold-start candidate.
// Restored SQL remains the user's desired configuration, while versions and
// acknowledged reset boundaries cannot roll behind the retained journal.
func recoverAuthorityDesiredState(ctx context.Context, config *conf.ClientPolicyConfig) (result error) {
	state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, state.Journal.Close()) }()
	if state.SourceID != config.InstanceID {
		return ErrClientPolicyLedger
	}
	expected := database.GetDB()
	if err := recoverAuthorityMigrationHistory(ctx, expected, state.Journal); err != nil {
		return err
	}
	after := ""
	for {
		accounts, err := state.Journal.AccountPage(after, 1000)
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			return nil
		}
		for _, account := range accounts {
			if err := recoverAuthorityAccount(ctx, expected, state.Journal, state.SourceID, account); err != nil {
				return err
			}
		}
		after = accounts[len(accounts)-1].Seed.ClientID
	}
}

func recoverAuthorityAccount(ctx context.Context, expected *gorm.DB, journal *policyauthority.Journal, source string, account policyauthority.Account) error {
	return runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		if account.Deleted {
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ClientPolicyTombstone{ClientID: account.Seed.ClientID}).Error
		}
		var client model.ClientRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id = ?", account.Seed.ClientID).First(&client).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := validateLocalClientPolicyResetScope(tx, []string{client.StableID}); err != nil {
			return err
		}
		if err := recoverAuthorityResetHistoryTx(tx, journal, source, account); err != nil {
			return err
		}
		protected, err := authorityProtectedReset(journal, account)
		if err != nil {
			return err
		}
		if protected != nil {
			latest, err := latestClientPolicyResets(tx, []string{client.StableID})
			if err != nil {
				return err
			}
			// Older writers could assign the same version to idle resets. SQL
			// restore regenerates IDs, so it cannot override the protected window.
			if reset := latest[client.StableID]; reset != nil && reset.PolicyVersion == protected.PolicyVersion && reset.RequestID != protected.RequestID {
				return ErrClientPolicyLedger
			}
		}
		if account.Policy == account.Seed.Policy {
			if client.DesiredPolicyVersion >= int64(account.Policy.Version) {
				return nil
			}
			record, err := journal.MigrationRecord("client-policy", client.StableID)
			if err != nil {
				return err
			}
			var original struct {
				Policy      clientpolicy.Policy
				Fingerprint string
			}
			if json.Unmarshal(record.Value, &original) != nil || original.Policy.Validate() != nil || original.Policy.ClientID != client.StableID || authorityPolicy(original.Policy, account.Policy.WindowID) != account.Policy {
				return ErrClientPolicyLedger
			}
			prior := original.Policy
			prior.Version = 1
			encoded, err := json.Marshal(prior)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(encoded)
			if original.Fingerprint != hex.EncodeToString(digest[:]) {
				return ErrClientPolicyLedger
			}
			return tx.Table("clients").Where("id = ?", client.Id).Updates(map[string]any{"desired_policy_version": int64(account.Policy.Version), "policy_fingerprint": original.Fingerprint}).Error
		}
		change, err := journal.LookupChange(client.StableID, fmt.Sprintf("desired:%d", account.Policy.Version))
		if err != nil {
			return err
		}
		if change.Request.Evidence == "" && client.DesiredPolicyVersion >= int64(account.Policy.Version) {
			window, err := authorityWindow(tx, client.StableID)
			if err != nil {
				return err
			}
			if window == account.Policy.WindowID {
				return nil
			}
		}
		evidence, err := decodeAuthorityEvidence(change, source)
		if err != nil {
			return err
		}
		if reset := evidence.Reset; reset != nil {
			if err := recoverAuthorityResetTx(tx, account, source, reset); err != nil {
				return err
			}
		}
		if client.DesiredPolicyVersion >= int64(account.Policy.Version) {
			return nil
		}
		if account.Policy.Version > math.MaxInt64 {
			return ErrClientPolicyLedger
		}
		prior := evidence.Policy
		prior.Version = 1 // Fingerprints exclude the monotone desired version.
		encoded, err := json.Marshal(prior)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(encoded)
		return tx.Table("clients").Where("id = ?", client.Id).Updates(map[string]any{"desired_policy_version": int64(account.Policy.Version), "policy_fingerprint": hex.EncodeToString(digest[:])}).Error
	})
}

func recoverAuthorityResetHistoryTx(tx *gorm.DB, journal *policyauthority.Journal, source string, account policyauthority.Account) error {
	after := ""
	for {
		page, err := journal.MigrationPage("resets", account.Seed.ClientID+"/", after, 128)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, record := range page {
			var reset model.ClientPolicyReset
			if json.Unmarshal(record.Value, &reset) != nil || record.Key != reset.ClientID+"/"+reset.RequestID {
				return ErrClientPolicyLedger
			}
			if err := recoverAuthorityResetTx(tx, account, source, &reset); err != nil {
				return err
			}
		}
		after = page[len(page)-1].Key
	}
	after = ""
	for {
		page, err := journal.ChangePage(account.Seed.ClientID, after, 128)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, change := range page {
			if change.Request.Evidence == "" {
				continue
			}
			evidence, err := decodeAuthorityEvidence(change, source)
			if err != nil {
				return err
			}
			if evidence.Reset != nil {
				if err := recoverAuthorityResetTx(tx, account, source, evidence.Reset); err != nil {
					return err
				}
			}
		}
		after = page[len(page)-1].Request.RequestID
	}
}

func recoverAuthorityResetTx(tx *gorm.DB, account policyauthority.Account, source string, reset *model.ClientPolicyReset) error {
	if reset == nil || validateClientPolicyReset(reset) != nil || reset.ClientID != account.Seed.ClientID || reset.InstanceID != source || reset.PolicyVersion <= 0 || uint64(reset.PolicyVersion) > account.Policy.Version || uint64(reset.RawUpload) > account.Usage.RawUpload || uint64(reset.RawDownload) > account.Usage.RawDownload || uint64(reset.BilledBytes) > account.Usage.BilledBytes || uint64(reset.BilledBytes) == account.Usage.BilledBytes && uint64(reset.Remainder) > account.Usage.Remainder {
		return ErrClientPolicyLedger
	}
	var existing model.ClientPolicyReset
	err := tx.Where("client_id = ? AND request_id = ?", reset.ClientID, reset.RequestID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		copy := *reset
		copy.Id = 0
		return tx.Create(&copy).Error
	}
	if err != nil {
		return err
	}
	existing.Id = reset.Id
	if existing != *reset {
		return ErrClientPolicyLedger
	}
	return nil
}
