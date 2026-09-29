package service

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

// How long a client stays recoverable after a node merge concluded it is gone.
// Any merge that sees it attached again inside this window clears the mark.
const syncOrphanReapGrace = 15 * time.Minute

// A traffic row with no clients row behind it is drift the orphan sweep will
// never mark, so it stays on the old delete-immediately path.
func clientRecordExists(tx *gorm.DB, email string) bool {
	var n int64
	if err := tx.Model(&model.ClientRecord{}).Where("email = ?", email).Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

func markSyncOrphan(tx *gorm.DB, email string, nowMs int64) error {
	if email == "" {
		return nil
	}
	return tx.Model(&model.ClientRecord{}).
		Where("email = ? AND sync_orphaned_at = 0", email).
		Update("sync_orphaned_at", nowMs).Error
}

// A client that is attached again is not orphaned, whatever an earlier merge
// concluded — this is what makes a bad merge recoverable instead of fatal.
func clearSyncOrphanMarks(tx *gorm.DB) error {
	return tx.Model(&model.ClientRecord{}).
		Where("sync_orphaned_at > 0 AND EXISTS (SELECT 1 FROM client_inbounds WHERE client_inbounds.client_id = clients.id)").
		Update("sync_orphaned_at", 0).Error
}

// ReapSyncOrphans deletes the clients the node-snapshot sweep marked and no
// later merge reclaimed. It is the only path that hard-deletes for that sweep.
func (s *ClientService) ReapSyncOrphans() (int, error) {
	db := database.GetDB()
	cutoff := time.Now().Add(-syncOrphanReapGrace).UnixMilli()
	var candidates []int
	if err := db.Model(&model.ClientRecord{}).
		Where("sync_orphaned_at > 0 AND sync_orphaned_at <= ?", cutoff).
		Where("NOT EXISTS (SELECT 1 FROM client_inbounds WHERE client_inbounds.client_id = clients.id)").
		Pluck("id", &candidates).Error; err != nil {
		return 0, err
	}
	reaped := 0
	for _, batch := range chunkInts(candidates, sqlInChunk) {
		var deleted []model.ClientRecord
		if err := runSerializedTx(func(tx *gorm.DB) error {
			var err error
			deleted, err = currentClientPolicyOrphans(tx, batch, cutoff)
			if err != nil || len(deleted) == 0 {
				return err
			}
			var ids []int
			var emails, subIDs []string
			for _, row := range deleted {
				ids = append(ids, row.Id)
				emails = append(emails, row.Email)
				subIDs = append(subIDs, row.SubID)
			}
			if err := recordClientPolicyTombstones(tx, ids); err != nil {
				return err
			}
			if err := adjustGroupBaselinesForRemovedTraffic(tx, emails); err != nil {
				return err
			}
			if err := clearClientHwidsBySubIDTx(tx, subIDs...); err != nil {
				return err
			}
			if err := tx.Where("client_id IN ?", ids).Delete(&model.ClientExternalLink{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", ids).Delete(&model.ClientRecord{}).Error; err != nil {
				return err
			}
			if err := tx.Where("email IN ?", emails).Delete(&xray.ClientTraffic{}).Error; err != nil {
				return err
			}
			if err := tx.Where("email IN ?", emails).Delete(&model.NodeClientTraffic{}).Error; err != nil {
				return err
			}
			return tx.Where("client_email IN ?", emails).Delete(&model.InboundClientIps{}).Error
		}); err != nil {
			return reaped, err
		}
		if len(deleted) == 0 {
			continue
		}
		ids := make([]string, 0, len(deleted))
		for _, row := range deleted {
			ids = append(ids, row.StableID)
		}
		if err := reconcileDeletedClientPolicies(ids); err != nil {
			return reaped, err
		}
		reaped += len(deleted)
		logger.Infof("reaped %d client(s) confirmed removed on their node", len(deleted))
	}
	return reaped, nil
}
