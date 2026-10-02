package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func authorityPolicy(policy clientpolicy.Policy, window string) policyauthority.Policy {
	direction := func(rate uint64) policyauthority.Direction {
		if rate == 0 {
			return policyauthority.Direction{Unlimited: true}
		}
		return policyauthority.Direction{Rate: rate, Burst: policy.BurstBytes}
	}
	return policyauthority.Policy{WindowID: window, Version: policy.Version, QuotaUnlimited: policy.QuotaBytes == 0, QuotaBytes: policy.QuotaBytes, Upload: direction(policy.UploadRate), Download: direction(policy.DownloadRate)}
}

func authorityWindow(tx *gorm.DB, client string) (string, error) {
	resets, err := latestClientPolicyResets(tx, []string{client})
	if err != nil {
		return "", err
	}
	window := "initial:" + client
	if reset := resets[client]; reset != nil {
		digest := sha256.Sum256([]byte(client + "/" + reset.RequestID))
		window = "reset:" + hex.EncodeToString(digest[:])
	}
	return window, nil
}

func validateAuthorityDesiredPolicyTx(tx *gorm.DB, policy clientpolicy.Policy) error {
	if !isSerializedTx(tx) {
		return ErrClientPolicyLedger
	}
	var client model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id = ?", policy.ClientID).First(&client).Error; err != nil {
		return err
	}
	if err := rejectDeletedClientPolicies(tx, []string{policy.ClientID}); err != nil {
		return err
	}
	if err := validateLocalClientPolicyResetScope(tx, []string{policy.ClientID}); err != nil {
		return err
	}
	resets, err := latestClientPolicyResets(tx, []string{policy.ClientID})
	if err != nil {
		return err
	}
	current, err := prepareClientPolicyRecord(tx, client, resets[policy.ClientID])
	if err != nil {
		return err
	}
	if current != policy {
		return ErrManagedConfigStale
	}
	return nil
}

func (a *managedAuthority) provisionPolicy(ctx context.Context, policy clientpolicy.Policy) error {
	if _, err := prepareClientPolicyLedgerForDatabase(ctx, a.db, a.config.InstanceID, policy.ClientID); err != nil {
		return err
	}
	return runSerializedTxContextForDatabase(ctx, a.db, func(tx *gorm.DB) error {
		if err := validateAuthorityDesiredPolicyTx(tx, policy); err != nil {
			return err
		}
		var receipts []model.ClientPolicyReceipt
		if err := tx.Where("client_id = ?", policy.ClientID).Find(&receipts).Error; err != nil {
			return err
		}
		var remainder int64
		for _, receipt := range receipts {
			if receipt.InstanceID != a.config.InstanceID || receipt.Sequence != 0 || receipt.Epoch != 0 || receipt.ReservedBytes != 0 || receipt.Revoked {
				return ErrClientPolicyLedger
			}
			remainder = receipt.Remainder
		}
		var total model.ClientPolicyTotal
		if err := tx.Where("client_id = ?", policy.ClientID).First(&total).Error; err != nil {
			return err
		}
		if total.RawUpload < 0 || total.RawDownload < 0 || total.BilledBytes < 0 || total.UncertainBytes < 0 || remainder < 0 || remainder >= 1000000 || policy.QuotaBaselineBytes != 0 || policy.QuotaBaselineRemainder != 0 || uint64(total.BilledBytes) > math.MaxInt64-uint64(total.UncertainBytes) {
			return ErrClientPolicyLedger
		}
		if err := rejectDeletedClientPolicies(tx, []string{policy.ClientID}); err != nil {
			return err
		}
		seed := policyauthority.Seed{ClientID: policy.ClientID, Policy: authorityPolicy(policy, "initial:"+policy.ClientID), Usage: policyauthority.Usage{RawUpload: uint64(total.RawUpload), RawDownload: uint64(total.RawDownload), BilledBytes: uint64(total.BilledBytes), Remainder: uint64(remainder)}, WindowUsed: uint64(total.BilledBytes), WindowRemainder: uint64(remainder), FrozenBilled: uint64(total.UncertainBytes)}
		if err := a.state.Journal.AddAccount(seed); err != nil {
			return err
		}
		return projectClientPolicyAuthorityTx(tx, a.state.Journal, policy.ClientID)
	})
}

func (a *managedAuthority) reconcilePolicy(ctx context.Context, policy clientpolicy.Policy, account policyauthority.Account) error {
	return runSerializedTxContextForDatabase(ctx, a.db, func(tx *gorm.DB) error {
		if err := validateAuthorityDesiredPolicyTx(tx, policy); err != nil {
			return err
		}
		window, err := authorityWindow(tx, policy.ClientID)
		if err != nil {
			return err
		}
		next := authorityPolicy(policy, window)
		if account.Policy == next {
			return projectClientPolicyAuthorityTx(tx, a.state.Journal, policy.ClientID)
		}
		if account.Deleted || next.Version <= account.Policy.Version {
			return ErrClientPolicyLedger
		}
		evidence, err := authorityEvidenceTx(tx, a.config.InstanceID, policy)
		if err != nil {
			return err
		}
		request := policyauthority.ChangeRequest{Identity: a.state.Journal.Identity(), ClientID: policy.ClientID, RequestID: fmt.Sprintf("desired:%d", next.Version), ExpectedVersion: account.Policy.Version, Policy: next, Reset: window != account.Policy.WindowID, Evidence: evidence}
		if request.Reset {
			resets, err := latestClientPolicyResets(tx, []string{policy.ClientID})
			if err != nil {
				return err
			}
			reset := resets[policy.ClientID]
			if reset == nil {
				return ErrClientPolicyLedger
			}
			request.HasResetBaseline = true
			request.ResetBaseline = policyauthority.Usage{RawUpload: uint64(reset.RawUpload), RawDownload: uint64(reset.RawDownload), BilledBytes: uint64(reset.BilledBytes), Remainder: uint64(reset.Remainder)}
		}
		_, err = a.state.Journal.ChangePolicy(request)
		if err != nil {
			return err
		}
		return projectClientPolicyAuthorityTx(tx, a.state.Journal, policy.ClientID)
	})
}

// SQL deletion intent must precede the durable tombstone. The tombstone blocks
// issuance without releasing uncertain capacity; only a core seal can do that.
func (a *managedAuthority) reconcileDeletionsLocked(ctx context.Context, ids []string) error {
	after := ""
	for {
		var deleted []string
		var settle []string
		err := runSerializedTxContextForDatabase(ctx, a.db, func(tx *gorm.DB) error {
			query := tx.Model(&model.ClientPolicyTombstone{}).Where("client_id > ?", after)
			if len(ids) != 0 {
				query = query.Where("client_id IN ?", ids)
			}
			if err := query.Order("client_id").Limit(1000).Pluck("client_id", &deleted).Error; err != nil {
				return err
			}
			for _, id := range deleted {
				if _, err := a.state.Journal.LookupAccount(id); errors.Is(err, policyauthority.ErrNotFound) {
					continue
				} else if err != nil {
					return err
				}
				if err := a.state.Journal.Tombstone(id); err != nil {
					return err
				}
				if err := projectClientPolicyAuthorityTx(tx, a.state.Journal, id); err != nil {
					return err
				}
				settle = append(settle, id)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if a.controller != nil {
			for _, id := range settle {
				if err := a.controller.suspendClient(ctx, id, false); err != nil {
					return err
				}
			}
		}
		if len(deleted) < 1000 {
			return nil
		}
		after = deleted[len(deleted)-1]
	}
}

func (a *managedAuthority) ReconcileDeletions(ctx context.Context, ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrClientPolicyLedger
	}
	for _, batch := range chunkStrings(ids, sqlInChunk) {
		if err := a.reconcileDeletionsLocked(ctx, batch); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return a.reconcileDeletionsLocked(ctx, nil)
	}
	return nil
}
