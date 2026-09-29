package service

import (
	"encoding/json"
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func recordClientTrafficResetTimes(tx *gorm.DB, ids []string, at int64) error {
	for _, batch := range chunkStrings(ids, sqlInChunk) {
		rows := make([]model.ClientTrafficResetTime, len(batch))
		for i, id := range batch {
			rows[i] = model.ClientTrafficResetTime{ClientID: id, EffectiveAt: at}
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "client_id"}},
			DoUpdates: clause.Assignments(map[string]any{"effective_at": gorm.Expr(
				"CASE WHEN excluded.effective_at > client_traffic_reset_times.effective_at THEN excluded.effective_at ELSE client_traffic_reset_times.effective_at END")}),
		}).Create(&rows).Error; err != nil {
			return err
		}
	}
	return nil
}

func scheduledResetEligibleClients(tx *gorm.DB, records []model.ClientRecord, operation model.ClientTrafficResetBatch, managed []string) ([]model.ClientRecord, error) {
	var targets []clientResetTarget
	if err := json.Unmarshal([]byte(operation.TargetsJSON), &targets); err != nil {
		return nil, err
	}
	ownCycle := make(map[string]bool)
	for _, target := range targets {
		ownCycle[target.ClientID] = target.EnableLegacy
	}
	var disabled []string
	for _, record := range records {
		if !record.Enable && ownCycle[record.StableID] {
			disabled = append(disabled, record.Email)
		}
	}
	depleted, err := depletedClientResetEmails(tx, disabled)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(records))
	for i, record := range records {
		ids[i] = record.StableID
	}
	latest := make(map[string]int64)
	for _, batch := range chunkStrings(ids, sqlInChunk) {
		var times []model.ClientTrafficResetTime
		if err := tx.Where("client_id IN ?", batch).Find(&times).Error; err != nil {
			return nil, err
		}
		for _, stamp := range times {
			latest[stamp.ClientID] = stamp.EffectiveAt
		}
		var history []model.ClientTrafficResetTime
		if err := tx.Model(&model.ClientPolicyReset{}).Select("client_id, MAX(created_at) AS effective_at").Where("client_id IN ?", batch).Group("client_id").Scan(&history).Error; err != nil {
			return nil, err
		}
		for _, stamp := range history {
			if _, exists := latest[stamp.ClientID]; !exists {
				latest[stamp.ClientID] = stamp.EffectiveAt
			}
		}
	}
	var eligible []model.ClientRecord
	for _, record := range records {
		_, prepared := slices.BinarySearch(managed, record.StableID)
		if !prepared && ownCycle[record.StableID] && !record.Enable && !depleted[record.Email] {
			continue
		}
		if latest[record.StableID] < operation.ScheduledAt {
			eligible = append(eligible, record)
		}
	}
	return eligible, nil
}
