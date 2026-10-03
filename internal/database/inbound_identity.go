package database

import (
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
)

// Backfill before AutoMigrate creates the unique index on historical tables.
func migrateInboundStableIDColumn() error {
	return migrateInboundStableIDColumnForDatabase(db)
}

func migrateInboundStableIDColumnForDatabase(database *gorm.DB) error {
	if !database.Migrator().HasTable(&model.Inbound{}) {
		return nil
	}
	return database.Transaction(func(tx *gorm.DB) error {
		if !tx.Migrator().HasColumn(&model.Inbound{}, "stable_id") {
			if err := tx.Migrator().AddColumn(&model.Inbound{}, "StableID"); err != nil {
				return err
			}
		}
		return migrateInboundStableIDs(tx)
	})
}

func migrateInboundStableIDs(tx *gorm.DB) error {
	var duplicates []string
	if err := tx.Table("inbounds").Where("stable_id IS NOT NULL AND stable_id <> ''").Group("stable_id").Having("COUNT(*) > 1").Limit(1).Pluck("stable_id", &duplicates).Error; err != nil {
		return err
	}
	if len(duplicates) != 0 {
		return fmt.Errorf("duplicate retained inbound identity")
	}
	var lastID *int
	for {
		var rows []struct {
			ID       int
			StableID sql.NullString
		}
		query := tx.Table("inbounds").Select("id, stable_id").Order("id").Limit(256)
		if lastID != nil {
			query = query.Where("id > ?", *lastID)
		}
		if err := query.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.StableID.Valid && row.StableID.String != "" {
				id, err := uuid.Parse(row.StableID.String)
				if err != nil || id == uuid.Nil || id.String() != row.StableID.String {
					return fmt.Errorf("invalid retained inbound identity at row %d", row.ID)
				}
				continue
			}
			id, err := uuid.NewRandom()
			if err != nil {
				return fmt.Errorf("generate migrated inbound identity: %w", err)
			}
			if err := tx.Exec("UPDATE inbounds SET stable_id = ? WHERE id = ? AND (stable_id IS NULL OR stable_id = '')", id.String(), row.ID).Error; err != nil {
				return err
			}
		}
		last := rows[len(rows)-1].ID
		lastID = &last
	}
}
