package service

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func validTrafficConfigDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, ch := range digest {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// The process receipt is locked first. This upsert locks its source before
// counter rows; conflicting identities never replace even unknown evidence.
func retainLegacyTrafficConfigSource(tx *gorm.DB, batch *xray.TrafficBatch, previousSequence int64) error {
	if batch.ConfigProof != nil && previousSequence > 0 {
		// An old receipt without a source row represents unknown history,
		// not a fresh child. The locked receipt serializes this existence check.
		var count int64
		if err := tx.Model(&model.LegacyTrafficConfigSource{}).Where("process_id = ?", batch.ProcessID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrLegacyTrafficRetention
		}
	}
	source := model.LegacyTrafficConfigSource{ProcessID: batch.ProcessID}
	if batch.ConfigProof != nil {
		source.ConfigDigest = batch.ConfigProof.ConfigDigest
		source.EffectiveConfigDigest = batch.ConfigProof.EffectiveConfigDigest
		source.ConfigStable = batch.ConfigProof.ConfigStable
	}
	result := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "process_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"config_stable": gorm.Expr("legacy_traffic_config_sources.config_stable AND excluded.config_stable"),
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL: "legacy_traffic_config_sources.config_digest = excluded.config_digest AND " +
				"legacy_traffic_config_sources.effective_config_digest = excluded.effective_config_digest",
		}}},
	}).Create(&source)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLegacyTrafficRetention
	}
	return nil
}
