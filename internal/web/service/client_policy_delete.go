package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func lockClientPolicyDeletionRecords(tx *gorm.DB, recordIDs []int) ([]model.ClientRecord, error) {
	var ids []string
	for _, batch := range chunkInts(recordIDs, sqlInChunk) {
		var next []string
		if err := tx.Model(&model.ClientRecord{}).Where("id IN ?", batch).Pluck("stable_id", &next).Error; err != nil {
			return nil, err
		}
		ids = append(ids, next...)
	}
	slices.Sort(ids)
	var records []model.ClientRecord
	for _, batch := range chunkStrings(ids, sqlInChunk) {
		var next []model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", batch).Order("stable_id").Find(&next).Error; err != nil {
			return nil, err
		}
		records = append(records, next...)
	}
	return records, nil
}

func recordClientPolicyTombstones(tx *gorm.DB, recordIDs []int) error {
	records, err := lockClientPolicyDeletionRecords(tx, recordIDs)
	if err != nil {
		return err
	}
	for start := 0; start < len(records); start += sqlInChunk {
		batch := records[start:min(start+sqlInChunk, len(records))]
		rows := make([]model.ClientPolicyTombstone, 0, len(batch))
		for _, record := range batch {
			rows = append(rows, model.ClientPolicyTombstone{ClientID: record.StableID})
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return err
		}
	}
	return nil
}

func currentClientPolicyOrphans(tx *gorm.DB, ids []int, cutoff int64) ([]model.ClientRecord, error) {
	if _, err := lockClientPolicyDeletionRecords(tx, ids); err != nil {
		return nil, err
	}
	var rows []model.ClientRecord
	for _, batch := range chunkInts(ids, sqlInChunk) {
		query := tx.Where("id IN ?", batch).Where("NOT EXISTS (SELECT 1 FROM client_inbounds WHERE client_inbounds.client_id = clients.id)")
		if cutoff > 0 {
			query = query.Where("sync_orphaned_at > 0 AND sync_orphaned_at <= ?", cutoff)
		}
		var next []model.ClientRecord
		if err := query.Find(&next).Error; err != nil {
			return nil, err
		}
		rows = append(rows, next...)
	}
	return rows, nil
}

func rejectDeletedClientPolicies(tx *gorm.DB, ids []string) error {
	for _, batch := range chunkStrings(ids, sqlInChunk) {
		var count int64
		if err := tx.Model(&model.ClientPolicyTombstone{}).Where("client_id IN ?", batch).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return clientpolicy.ErrRevoked
		}
	}
	return nil
}

func pendingClientPolicyDeletions(ctx context.Context, instanceID, after string, initializedOnly bool) ([]string, error) {
	query := database.GetDB().WithContext(ctx).Table("client_policy_tombstones AS deleted").
		Joins("JOIN client_policy_receipts AS receipt ON receipt.client_id = deleted.client_id").
		Where("receipt.instance_id = ? AND receipt.revoked = ? AND deleted.client_id > ?", instanceID, false, after).
		Where("receipt.deletion_absent = ? OR receipt.policy_version > 0", false)
	if initializedOnly {
		query = query.Where("receipt.policy_version > 0")
	}
	var ids []string
	err := query.Order("deleted.client_id").Limit(1000).Pluck("deleted.client_id", &ids).Error
	return ids, err
}

func confirmAbsentClientPolicyDeletions(ctx context.Context, instanceID string, ids []string) error {
	return runSerializedTx(func(tx *gorm.DB) error {
		for _, batch := range chunkStrings(ids, sqlInChunk) {
			query := tx.WithContext(ctx).Model(&model.ClientPolicyReceipt{}).
				Where("instance_id = ? AND client_id IN ? AND policy_version = 0", instanceID, batch).
				Where("EXISTS (SELECT 1 FROM client_policy_tombstones AS deleted WHERE deleted.client_id = client_policy_receipts.client_id)")
			if err := query.Update("deletion_absent", true).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func reconcileDeletedClientPolicies(clientIDs []string) (resultErr error) {
	lock.Lock()
	defer lock.Unlock()
	process := currentXrayProcess()
	if process == nil || !process.IsRunning() || len(process.GetConfig().ClientPolicy) == 0 {
		return nil
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(panelruntime.ErrManagedApply, resultErr, process.Stop())
			(&XrayService{}).SetToNeedRestart()
		}
	}()
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var ids []string
	var err error
	if len(clientIDs) == 0 {
		ids, err = pendingClientPolicyDeletions(ctx, config.InstanceID, "", true)
	} else {
		for _, batch := range chunkStrings(clientIDs, sqlInChunk) {
			var next []string
			err = database.GetDB().Table("client_policy_tombstones AS deleted").
				Joins("JOIN client_policy_receipts AS receipt ON receipt.client_id = deleted.client_id").
				Where("receipt.instance_id = ? AND receipt.revoked = ? AND deleted.client_id IN ?", config.InstanceID, false, batch).
				Order("deleted.client_id").Pluck("deleted.client_id", &next).Error
			if err != nil {
				break
			}
			ids = append(ids, next...)
		}
	}
	if err != nil || len(ids) == 0 {
		return err
	}
	managed, err := localManagedPolicyRuntime()
	if err != nil {
		return err
	}
	runtime, ok := managed.(panelruntime.ManagedDeletionRuntime)
	if !ok {
		return errors.New("Runtime does not support permanent managed identity deletion")
	}
	for _, batch := range chunkStrings(ids, 1000) {
		var absent []string
		absent, err = runtime.RevokeManagedClients(ctx, process, batch)
		if err == nil && len(absent) > 0 {
			err = confirmAbsentClientPolicyDeletions(ctx, config.InstanceID, absent)
		}
		if err != nil {
			break
		}
	}
	err = errors.Join(err, pollLocalClientPolicyLedger(ctx, process))
	return err
}
