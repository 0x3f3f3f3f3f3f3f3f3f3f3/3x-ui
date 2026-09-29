package database

import (
	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
)

func migrateClientStableIDColumn() error {
	if !db.Migrator().HasTable(&model.ClientRecord{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&model.ClientRecord{}, "stable_id") {
		if err := db.Migrator().AddColumn(&model.ClientRecord{}, "StableID"); err != nil {
			return err
		}
	}
	return migrateClientStableIDs()
}

func migrateClientStableIDs() error {
	return db.Transaction(func(tx *gorm.DB) error {
		for {
			var ids []int
			if err := tx.Table("clients").Where("stable_id IS NULL OR stable_id = ''").Order("id").Limit(256).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			for _, rowID := range ids {
				id, err := uuid.NewRandom()
				if err != nil {
					return err
				}
				if err := tx.Exec("UPDATE clients SET stable_id = ? WHERE id = ? AND (stable_id IS NULL OR stable_id = '')", id.String(), rowID).Error; err != nil {
					return err
				}
			}
		}
	})
}
