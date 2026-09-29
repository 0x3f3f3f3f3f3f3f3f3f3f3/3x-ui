package service

import (
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func validateAttachmentOwners(clients []model.Client, owners map[string]*model.ClientRecord) error {
	if owners == nil {
		return nil
	}
	seen := make(map[string]bool, len(clients))
	for _, client := range clients {
		owner := owners[client.Email]
		key := strings.ToLower(client.Email)
		if owner == nil || owner.Id <= 0 || owner.PolicyID == "" || owner.Email != client.Email || owner.SubID != client.SubID || seen[key] {
			return database.ErrUsageConflict
		}
		seen[key] = true
	}
	return nil
}

func lockAttachmentOwnersTx(tx *gorm.DB, clients []model.Client, owners map[string]*model.ClientRecord) error {
	if owners == nil {
		return nil
	}
	ids := make([]int, 0, len(clients))
	for _, client := range clients {
		ids = append(ids, owners[client.Email].Id)
	}
	slices.Sort(ids)
	for _, part := range chunkInts(ids, sqlInChunk) {
		if err := lockUsageClientsTx(tx, part); err != nil {
			return err
		}
		var current []model.ClientRecord
		if err := tx.Where("id IN ?", part).Find(&current).Error; err != nil {
			return err
		}
		for _, row := range current {
			owner := owners[row.Email]
			if owner == nil || row.Id != owner.Id || row.PolicyID != owner.PolicyID || row.SubID != owner.SubID {
				return database.ErrUsageConflict
			}
		}
	}
	return nil
}
