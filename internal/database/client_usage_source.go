package database

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Prepaid sources have no uncommitted forwarded bytes, so a new owner can fence the old incarnation.
func (l *ClientUsageLedger) ClaimAdmissionSource(ctx context.Context, policyID, source string) (model.ClientUsageMeter, error) {
	var result model.ClientUsageMeter
	if source == "" || len(source) > 200 || strings.TrimSpace(source) != source {
		return result, ErrUsageConflict
	}
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireAdmissionDurability(tx); err != nil {
			return err
		}
		_, account, err := lockClientUsage(tx, policyID)
		if err != nil {
			return err
		}
		var old model.ClientUsageMeter
		err = tx.Where("policy_id = ? AND source = ? AND closed = ?", policyID, source, false).First(&old).Error
		if err == nil {
			if !old.AdmissionOnly {
				return ErrUsageConflict
			}
			if err := tx.Model(&old).Update("closed", true).Error; err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		id, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		result = model.ClientUsageMeter{ID: id.String(), PolicyID: policyID, Source: source, Revision: account.Revision, Multiplier: account.Multiplier, AdmissionOnly: true}
		return tx.Create(&result).Error
	})
	if err != nil {
		return model.ClientUsageMeter{}, err
	}
	return result, nil
}

func (l *ClientUsageLedger) CheckAdmissionSource(ctx context.Context, meterID string) error {
	var hint model.ClientUsageMeter
	if err := l.db.WithContext(ctx).Where("meter_id = ?", meterID).First(&hint).Error; err != nil {
		return err
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireAdmissionDurability(tx); err != nil {
			return err
		}
		client, account, err := lockClientUsage(tx, hint.PolicyID)
		if err != nil {
			return err
		}
		var meter model.ClientUsageMeter
		if err := tx.Where("meter_id = ?", meterID).First(&meter).Error; err != nil {
			return err
		}
		if meter.Closed || !meter.AdmissionOnly || meter.Revision != account.Revision {
			return ErrUsageClosed
		}
		quota, err := clientAdmissionQuota(tx, client, &account)
		if err != nil || quota == 0 {
			return err
		}
		whole, carry, err := clientpolicy.Charge(1, clientpolicy.Multiplier(account.Multiplier), account.Remainder)
		if err != nil {
			return err
		}
		remaining := quota - account.Billed
		if whole > remaining || (whole == remaining && carry > 0) {
			return &UsageQuotaError{}
		}
		return nil
	})
}
