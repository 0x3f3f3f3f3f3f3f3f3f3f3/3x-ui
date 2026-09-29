package service

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var ErrLegacyHandoffInterrupted = errors.New("legacy traffic handoff is incomplete; recover its original final traffic receipt before activation")

func checkLocalLegacyHandoff(process *xray.Process) error {
	var source model.ClientPolicySource
	db := database.GetDB()
	if err := db.Where("node_key = ?", "local").First(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if source.HandoffBootID == "" {
		return nil
	}
	if source.HandoffBatchID != "" {
		return checkLegacyHandoffReceipt(db, &source)
	}
	if process == nil || process.TrafficDrainBootID() != source.HandoffBootID || !process.FinalTrafficPending() {
		return ErrLegacyHandoffInterrupted
	}
	return nil
}

// Commit intent before requesting the irreversible drain, including pending poll replay.
func beginLegacyHandoff(instanceID, boot string) error {
	if boot == "" {
		return ErrLegacyHandoffInterrupted
	}
	return runSerializedTx(func(tx *gorm.DB) error {
		var source model.ClientPolicySource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "instance_id = ? AND node_key = ?", instanceID, "local").Error; err != nil {
			return err
		}
		if source.HandoffBootID == boot {
			return nil
		}
		if source.HandoffBootID != "" || source.Epoch != 0 || source.Sequence != 0 {
			return ErrLegacyHandoffInterrupted
		}
		return tx.Model(&source).Update("handoff_boot_id", boot).Error
	})
}

// Completion is part of the same SQL transaction as the final usage and receipt.
func completeLegacyHandoff(tx *gorm.DB, instanceID, boot string, batch *xray.TrafficBatch) error {
	if !batch.Final {
		return nil
	}
	var source model.ClientPolicySource
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "instance_id = ?", instanceID).Error; err != nil {
		return err
	}
	if source.HandoffBootID != boot || source.HandoffBatchID != "" || batch.ProcessID == "" || batch.ID == "" {
		return ErrLegacyHandoffInterrupted
	}
	return tx.Model(&source).Updates(map[string]any{"handoff_process_id": batch.ProcessID, "handoff_batch_id": batch.ID}).Error
}

func checkLegacyHandoffReceipt(tx *gorm.DB, source *model.ClientPolicySource) error {
	if source.HandoffBatchID == "" || source.HandoffProcessID == "" {
		return ErrLegacyHandoffInterrupted
	}
	var receipt model.LegacyTrafficReceipt
	if err := tx.First(&receipt, "process_id = ?", source.HandoffProcessID).Error; err != nil {
		return errors.Join(ErrLegacyHandoffInterrupted, err)
	}
	if receipt.BatchID != source.HandoffBatchID || receipt.Sequence < 1 {
		return ErrLegacyHandoffInterrupted
	}
	return nil
}

func requireLegacyHandoffComplete(instanceID, boot string) error {
	var source model.ClientPolicySource
	db := database.GetDB()
	if err := db.First(&source, "instance_id = ?", instanceID).Error; err != nil {
		return err
	}
	if source.HandoffBootID != boot {
		return ErrLegacyHandoffInterrupted
	}
	return checkLegacyHandoffReceipt(db, &source)
}
