package database

import (
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Backfill before AutoMigrate builds the unique index; legacy usage stays in its existing rows.
func migrateClientPolicyIdentityColumn() error {
	migrator := db.Migrator()
	if !migrator.HasTable(&model.ClientRecord{}) {
		return nil
	}
	if !migrator.HasColumn(&model.ClientRecord{}, "PolicyID") {
		if err := migrator.AddColumn(&model.ClientRecord{}, "PolicyID"); err != nil {
			return err
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			Id int
		}
		return tx.Table("clients").Select("id").
			Where("policy_id IS NULL OR policy_id = ''").
			FindInBatches(&rows, 500, func(_ *gorm.DB, _ int) error {
				for _, row := range rows {
					id, err := uuid.NewRandom()
					if err != nil {
						return err
					}
					if err := tx.Table("clients").Where("id = ? AND (policy_id IS NULL OR policy_id = '')", row.Id).
						Update("policy_id", id.String()).Error; err != nil {
						return err
					}
				}
				return nil
			}).Error
	})
}
