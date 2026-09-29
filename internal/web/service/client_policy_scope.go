package service

import (
	"fmt"
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func guardRemoteClientPolicyAttachments(tx *gorm.DB, inboundID int, existing map[string]*model.ClientRecord) error {
	if len(existing) == 0 {
		return nil
	}
	var inbound struct{ NodeID *int }
	if err := tx.Model(&model.Inbound{}).Select("node_id").First(&inbound, inboundID).Error; err != nil {
		return err
	}
	if inbound.NodeID == nil {
		return nil
	}
	ids := make([]string, 0, len(existing))
	for _, record := range existing {
		ids = append(ids, record.StableID)
	}
	slices.Sort(ids)
	// Match policy preparation's stable-ID lock order, including across batches.
	for _, batch := range chunkStrings(ids, 400) {
		var rows []model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", batch).Order("stable_id").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != len(batch) {
			return ErrManagedConfigStale
		}
		for i := range rows {
			row := &rows[i]
			if row.DesiredPolicyVersion != 0 {
				return fmt.Errorf("%w: local managed policy cannot attach to remote nodes without coordinated budgets", ErrClientPolicyLedger)
			}
			prior := existing[row.Email]
			if prior == nil || prior.StableID != row.StableID {
				return ErrManagedConfigStale
			}
			existing[row.Email] = row
		}
	}
	return nil
}
