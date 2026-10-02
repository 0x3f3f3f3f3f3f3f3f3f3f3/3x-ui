package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func projectClientPolicyAuthority(ctx context.Context, expected *gorm.DB, journal *policyauthority.Journal, clientID string) error {
	if ctx == nil || journal == nil {
		return ErrClientPolicyLedger
	}
	return runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		return projectClientPolicyAuthorityTx(tx, journal, clientID)
	})
}

func issueClientPolicyAuthority(ctx context.Context, expected *gorm.DB, journal *policyauthority.Journal, request policyauthority.Request) (policyauthority.Grant, error) {
	if ctx == nil || journal == nil {
		return policyauthority.Grant{}, ErrClientPolicyLedger
	}
	var grant policyauthority.Grant
	err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		account, err := journal.Account(request.Binding.ClientID)
		if err != nil {
			return err
		}
		if err := validateAuthorityProjectionClient(tx, account); err != nil {
			return err
		}
		if _, err := checkedAuthorityProjection(tx, journal, account); err != nil {
			return err
		}
		// The journal's synchronous commit precedes the disposable SQL write.
		// SQL failure leaves capacity held; the exact request reuses that grant.
		grant, err = journal.Issue(request)
		if err != nil {
			return err
		}
		return projectClientPolicyAuthorityTx(tx, journal, request.Binding.ClientID)
	})
	if err != nil {
		return policyauthority.Grant{}, err
	}
	return grant, nil
}

func validateAuthorityProjectionClient(tx *gorm.DB, account policyauthority.Account) error {
	id, err := uuid.Parse(account.Seed.ClientID)
	if err != nil || id.String() != account.Seed.ClientID {
		return ErrClientPolicyLedger
	}
	var tombstones int64
	if err := tx.Model(&model.ClientPolicyTombstone{}).Where("client_id = ?", account.Seed.ClientID).Count(&tombstones).Error; err != nil {
		return err
	}
	if account.Deleted {
		return nil
	}
	if tombstones != 0 {
		return ErrClientPolicyLedger
	}
	var client model.ClientRecord
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("stable_id").First(&client, "stable_id = ?", account.Seed.ClientID).Error
}

func projectClientPolicyAuthorityTx(tx *gorm.DB, journal *policyauthority.Journal, clientID string) error {
	if !isSerializedTx(tx) {
		return ErrClientPolicyLedger
	}
	account, err := journal.Account(clientID)
	if err != nil {
		return err
	}
	if err := validateAuthorityProjectionClient(tx, account); err != nil {
		return err
	}
	identity := journal.Identity()
	if identity.Generation == 0 || identity.Generation > math.MaxInt64 || account.Revision == 0 || account.Revision > math.MaxInt64 {
		return ErrClientPolicyLedger
	}
	reset, err := authorityProtectedReset(journal, account)
	if err != nil {
		return err
	}
	if reset != nil {
		var source model.ClientPolicySource
		if err := tx.Where("node_key = ?", "local").First(&source).Error; err != nil {
			return err
		}
		if source.InstanceID != reset.InstanceID {
			return ErrClientPolicyLedger
		}
	}
	encoded, err := json.Marshal(authorityAccountProjection{Account: account, ProtectedReset: reset})
	if err != nil {
		return err
	}
	row := model.ClientPolicyAuthorityProjection{ClientID: clientID, AuthorityID: identity.AuthorityID, Generation: int64(identity.Generation), Revision: int64(account.Revision), AccountJSON: string(encoded)}
	previous, err := checkedAuthorityProjection(tx, journal, account)
	if err != nil {
		return err
	}
	if previous.ClientID != "" {
		if previous.Revision != row.Revision || previous.AccountJSON != row.AccountJSON {
			result := tx.Model(&model.ClientPolicyAuthorityProjection{}).Where("client_id = ? AND revision = ?", clientID, previous.Revision).Updates(map[string]any{"revision": row.Revision, "account_json": row.AccountJSON})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrClientPolicyLedger
			}
		}
	} else {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	if account.Deleted {
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ClientPolicyTombstone{ClientID: clientID}).Error
	}
	return nil
}

func checkedAuthorityProjection(tx *gorm.DB, journal *policyauthority.Journal, account policyauthority.Account) (model.ClientPolicyAuthorityProjection, error) {
	var previous model.ClientPolicyAuthorityProjection
	identity := journal.Identity()
	if !isSerializedTx(tx) || identity.Generation == 0 || identity.Generation > math.MaxInt64 || account.Revision == 0 || account.Revision > math.MaxInt64 {
		return previous, ErrClientPolicyLedger
	}
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&previous, "client_id = ?", account.Seed.ClientID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ClientPolicyAuthorityProjection{}, nil
	}
	if err != nil {
		return previous, err
	}
	var prior policyauthority.Account
	if previous.AuthorityID != identity.AuthorityID || previous.Generation != int64(identity.Generation) || previous.Revision <= 0 || previous.Revision > int64(account.Revision) || json.Unmarshal([]byte(previous.AccountJSON), &prior) != nil || prior.Seed != account.Seed || prior.Revision != uint64(previous.Revision) || prior.Policy.Version > account.Policy.Version || prior.Deleted && !account.Deleted || prior.Usage.RawUpload > account.Usage.RawUpload || prior.Usage.RawDownload > account.Usage.RawDownload || prior.Usage.BilledBytes > account.Usage.BilledBytes || prior.Usage.BilledBytes == account.Usage.BilledBytes && prior.Usage.Remainder > account.Usage.Remainder || previous.Revision == int64(account.Revision) && prior != account {
		return previous, ErrClientPolicyLedger
	}
	return previous, nil
}
