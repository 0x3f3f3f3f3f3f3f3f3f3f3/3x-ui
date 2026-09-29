package service

import (
	"errors"
	"math"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func activateClientPolicyFirstUse(tx *gorm.DB, receipt *command.LedgerRecord) error {
	if receipt.FirstUsedAt == 0 || receipt.Revoked {
		return nil
	}
	var client model.ClientRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&client, "stable_id = ?", receipt.ClientId).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if client.ExpiryTime >= 0 || client.DesiredPolicyVersion != int64(receipt.PolicyVersion) {
		return nil
	}
	if client.ExpiryTime == math.MinInt64 || receipt.FirstUsedAt > math.MaxInt64+client.ExpiryTime {
		return ErrClientPolicyLedger
	}
	if err := validateLocalClientPolicyResetScope(tx, []string{client.StableID}); err != nil {
		return err
	}
	expiry := receipt.FirstUsedAt - client.ExpiryTime
	now := time.Now().UnixMilli()
	if err := tx.Model(&client).Updates(map[string]any{"expiry_time": expiry, "updated_at": now}).Error; err != nil {
		return err
	}
	if err := tx.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("expiry_time", expiry).Error; err != nil {
		return err
	}
	if err := updateManagedRenewalInbounds(tx, map[string]int64{client.Email: expiry}, now); err != nil {
		return err
	}
	resets, err := latestClientPolicyResets(tx, []string{client.StableID})
	if err != nil {
		return err
	}
	client.ExpiryTime = expiry
	_, err = prepareClientPolicyRecord(tx, client, resets[client.StableID])
	return err
}
