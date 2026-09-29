package service

import (
	"context"
	"errors"
	"math"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func reconcileManagedUsageTx(tx *gorm.DB, ids []int, newIDs map[int]bool) error {
	for _, part := range chunkInts(ids, sqlInChunk) {
		var records []model.ClientRecord
		if err := tx.Where("id IN ?", part).Order("id").Find(&records).Error; err != nil {
			return err
		}
		for _, record := range records {
			var inbounds []model.Inbound
			if err := tx.Model(&model.Inbound{}).Joins("JOIN client_inbounds ci ON ci.inbound_id = inbounds.id").
				Where("ci.client_id = ?", record.Id).Find(&inbounds).Error; err != nil {
				return err
			}
			hasSSH, hasMieru, unsupported := false, false, false
			for _, inbound := range inbounds {
				hasSSH = hasSSH || inbound.Protocol == model.SSH
				hasMieru = hasMieru || inbound.Protocol == model.Mieru
				unsupported = unsupported || (inbound.Protocol != model.SSH && inbound.Protocol != model.Mieru) || inbound.NodeID != nil
			}
			owned, err := clientHasUsageAccount(tx, record.Email)
			if err != nil {
				return err
			}
			if !hasSSH && !hasMieru && !owned {
				continue
			}
			if unsupported {
				return errors.New("this managed client's policy cannot yet be enforced across its other protocol or remote attachments")
			}
			if !owned && !newIDs[record.Id] {
				return errors.New("existing clients require accounting migration before managed protocol attachment")
			}
			if hasSSH {
				if _, err := sshClientBinding(*record.ToClient(), record.PolicyID); err != nil {
					return err
				}
			}
			if hasMieru {
				if _, err := mieruClientBinding(*record.ToClient(), record.PolicyID); err != nil {
					return err
				}
			}
			if err := database.NewClientUsageLedger(tx).Ensure(tx.Statement.Context, record.PolicyID); err != nil {
				return err
			}
		}
	}
	return nil
}

func startManagedClient(ctx context.Context, policyID string) error {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var client model.ClientRecord
	if err := database.GetDB().WithContext(ctx).Where("policy_id = ?", policyID).First(&client).Error; err != nil {
		return err
	}
	if client.ExpiryTime >= 0 {
		return nil
	}
	return runSerializedTxContext(ctx, func(tx *gorm.DB) error {
		if err := lockUsageClientTx(tx, client.Id); err != nil {
			return err
		}
		if err := tx.Where("policy_id = ?", policyID).First(&client).Error; err != nil {
			return err
		}
		var traffic xray.ClientTraffic
		if err := tx.Where("policy_id = ? AND email = ?", policyID, client.Email).First(&traffic).Error; err != nil {
			return err
		}
		if !client.Enable || !traffic.Enable {
			return database.ErrUsageDisabled
		}
		if client.ExpiryTime >= 0 {
			return nil
		}
		if traffic.ExpiryTime != client.ExpiryTime {
			return database.ErrUsageUnready
		}
		now := time.Now().UnixMilli()
		if client.ExpiryTime < now-math.MaxInt64 {
			return errors.New("managed client first-use expiry duration overflows")
		}
		expiry := now - client.ExpiryTime
		if err := tx.Model(&client).Updates(map[string]any{"expiry_time": expiry, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&traffic).Update("expiry_time", expiry).Error; err != nil {
			return err
		}
		return updateManagedExpirySettingsTx(tx, client, expiry, newTrafficMutationBatch())
	})
}
