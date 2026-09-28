package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type depletionCandidate struct {
	xray.ClientTraffic
	ClientID       int
	ClientPolicyID string
}

func depletionCandidates(tx *gorm.DB, trafficIDs []int) ([]depletionCandidate, error) {
	query := depletedTrafficQuery(tx, time.Now().UnixMilli())
	if trafficIDs != nil {
		query = query.Where("client_traffics.id IN ?", trafficIDs)
	}
	var rows []depletionCandidate
	err := tx.Table("(?) AS depleted", query).
		Select("depleted.*, COALESCE(c.id, 0) AS client_id, COALESCE(c.policy_id, '') AS client_policy_id").
		Joins("LEFT JOIN clients c ON c.email = depleted.email").Find(&rows).Error
	return rows, err
}

func recheckDepletionTx(tx *gorm.DB, candidates []depletionCandidate) ([]depletionCandidate, error) {
	clientIDs := make([]int, 0, len(candidates))
	trafficIDs := make([]int, 0, len(candidates))
	byID := make(map[int]depletionCandidate, len(candidates))
	for _, candidate := range candidates {
		if candidate.ClientID != 0 {
			clientIDs = append(clientIDs, candidate.ClientID)
		}
		trafficIDs = append(trafficIDs, candidate.Id)
		byID[candidate.Id] = candidate
	}
	slices.Sort(clientIDs)
	clientIDs = slices.Compact(clientIDs)
	slices.Sort(trafficIDs)
	// Missing rows are expected after a concurrent delete; identities are checked again below.
	for _, lock := range []struct {
		model any
		ids   []int
	}{{&model.ClientRecord{}, clientIDs}, {&xray.ClientTraffic{}, trafficIDs}} {
		for _, part := range chunkInts(lock.ids, sqlInChunk) {
			query := tx.Model(lock.model).Where("id IN ?", part)
			if tx.Name() == "sqlite" {
				if err := query.UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
					return nil, err
				}
			} else {
				var locked []int
				if err := query.Order("id").Clauses(clause.Locking{Strength: "UPDATE"}).Pluck("id", &locked).Error; err != nil {
					return nil, err
				}
			}
		}
	}
	var current []depletionCandidate
	for _, part := range chunkInts(trafficIDs, sqlInChunk) {
		rows, err := depletionCandidates(tx, part)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			before := byID[row.Id]
			if row.Email == before.Email && row.PolicyID == before.PolicyID && row.ClientID == before.ClientID && row.ClientPolicyID == before.ClientPolicyID {
				current = append(current, row)
			}
		}
	}
	return current, nil
}

func (s *InboundService) purgeDepletedClients(scope int, deleteRecords bool) (int, bool, error) {
	db := database.GetDB()
	candidates, err := depletionCandidates(db, nil)
	if err != nil || len(candidates) == 0 {
		return 0, false, err
	}
	inboundIDs, err := depletionInboundIDs(db, candidates, scope)
	if err != nil {
		return 0, false, err
	}
	locks := make([]*sync.Mutex, 0, len(inboundIDs))
	for _, id := range inboundIDs {
		locks = append(locks, lockInbound(id))
	}
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}()
	batch := newTrafficMutationBatch()
	var deletedInbounds []model.Inbound
	var deletedEmails []string
	err = runSerializedTx(func(tx *gorm.DB) error {
		rows, err := recheckDepletionTx(tx, candidates)
		if err != nil {
			return err
		}
		if deleteRecords {
			rows = slices.DeleteFunc(rows, func(row depletionCandidate) bool { return row.ClientID == 0 })
		}
		if len(rows) == 0 {
			return nil
		}
		currentIDs, err := depletionInboundIDs(tx, rows, scope)
		if err != nil {
			return err
		}
		for _, id := range currentIDs {
			if _, locked := slices.BinarySearch(inboundIDs, id); !locked {
				return fmt.Errorf("inbounds changed during depletion cleanup; retry")
			}
		}
		wanted := make(map[string]int, len(rows))
		for _, row := range rows {
			wanted[row.Email] = row.ClientID
		}
		for _, part := range chunkInts(currentIDs, sqlInChunk) {
			var inbounds []model.Inbound
			if err := tx.Where("id IN ?", part).Order("id").Find(&inbounds).Error; err != nil {
				return err
			}
			for _, inbound := range inbounds {
				deleted, err := s.purgeDepletedInboundTx(tx, inbound, wanted, deleteRecords, batch)
				if err != nil {
					return err
				}
				if deleted {
					deletedInbounds = append(deletedInbounds, inbound)
				}
			}
		}
		if err := batch.markNodesTx(tx); err != nil {
			return err
		}
		if scope >= 0 {
			rows, err = unreferencedDepletionRows(tx, rows)
			if err != nil {
				return err
			}
		}
		emails := make([]string, 0, len(rows))
		for _, row := range rows {
			emails = append(emails, row.Email)
		}
		if err := s.delClientStatsByEmails(tx, emails); err != nil {
			return err
		}
		if err := s.delClientIPsByEmails(tx, emails); err != nil {
			return err
		}
		if deleteRecords {
			if err := deleteDepletedRecordsTx(tx, rows); err != nil {
				return err
			}
			deletedEmails = emails
			tombstoneClientEmails(deletedEmails)
		}
		return nil
	})
	if err != nil {
		withdrawClientTombstones(deletedEmails...)
		return 0, false, err
	}
	needRestart := s.applyTrafficMutationBatch(batch)
	needRestart = s.applyTrafficRemotePlans(batch.remotePlans) || needRestart
	for i := range deletedInbounds {
		inbound := &deletedInbounds[i]
		rt, err := s.runtimeFor(inbound)
		if err == nil {
			ctx, cancel := nodePushContext()
			err = rt.DelInbound(ctx, inbound)
			cancel()
		}
		if err != nil && !xray.IsMissingHandlerErr(err) {
			logger.Warning("depleted inbound runtime cleanup failed after commit:", err)
			needRestart = true
		}
		if inbound.Tag != "" {
			if _, err := (&XraySettingService{}).RemoveInboundTagReferences(inbound.Tag); err != nil {
				logger.Warning("depleted inbound routing cleanup failed after commit:", err)
			}
		}
	}
	return len(deletedEmails), needRestart, nil
}

func (s *InboundService) purgeDepletedInboundTx(tx *gorm.DB, inbound model.Inbound, wanted map[string]int, keepInbound bool, batch *trafficMutationBatch) (bool, error) {
	var settings map[string]any
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return false, err
	}
	clients, _ := settings["clients"].([]any)
	kept := make([]any, 0, len(clients))
	var removed []string
	var ids []int
	for _, client := range clients {
		_, email := clientEntryEmail(client)
		if id, found := wanted[email]; found && email != "" {
			removed = append(removed, email)
			if id != 0 {
				ids = append(ids, id)
			}
		} else {
			kept = append(kept, client)
		}
	}
	if len(removed) == 0 {
		return false, nil
	}
	if inbound.NodeID != nil {
		batch.addNode(*inbound.NodeID)
	}
	if len(kept) == 0 && !keepInbound {
		if err := s.clientService.DetachInbound(tx, inbound.Id); err != nil {
			return false, err
		}
		if err := tx.Where("inbound_id = ?", inbound.Id).Delete(&model.Host{}).Error; err != nil {
			return false, err
		}
		return true, tx.Delete(&model.Inbound{}, inbound.Id).Error
	}
	settings["clients"] = kept
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	updated := inbound
	updated.Settings = string(encoded)
	if err := tx.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("settings", updated.Settings).Error; err != nil {
		return false, err
	}
	for _, part := range chunkInts(ids, sqlInChunk) {
		if err := tx.Where("inbound_id = ? AND client_id IN ?", inbound.Id, part).Delete(&model.ClientInbound{}).Error; err != nil {
			return false, err
		}
	}
	if inbound.NodeID != nil {
		batch.remotePlans = append(batch.remotePlans, trafficInboundUpdatePlan{oldInbound: inbound, newInbound: updated})
	} else {
		for _, email := range removed {
			batch.localPlans = append(batch.localPlans, trafficLocalApplyPlan{action: trafficRemoveUser, inbound: updated, email: email})
		}
	}
	return false, nil
}

func depletionInboundIDs(tx *gorm.DB, rows []depletionCandidate, scope int) ([]int, error) {
	var ids []int
	if scope >= 0 {
		err := tx.Model(&model.Inbound{}).Where("id = ?", scope).Pluck("id", &ids).Error
		return ids, err
	}
	emails := make([]string, 0, len(rows))
	clientIDs := make([]int, 0, len(rows))
	for _, row := range rows {
		emails = append(emails, row.Email)
		if row.ClientID != 0 {
			clientIDs = append(clientIDs, row.ClientID)
		}
	}
	query := fmt.Sprintf("SELECT DISTINCT inbounds.id %s WHERE %s IN ?", database.JSONClientsFromInbound(), database.JSONFieldText("client.value", "email"))
	for _, part := range chunkStrings(emails, sqlInChunk) {
		var found []int
		if err := tx.Raw(query, part).Scan(&found).Error; err != nil {
			return nil, err
		}
		ids = append(ids, found...)
	}
	for _, part := range chunkInts(clientIDs, sqlInChunk) {
		var found []int
		if err := tx.Model(&model.ClientInbound{}).Where("client_id IN ?", part).Distinct("inbound_id").Pluck("inbound_id", &found).Error; err != nil {
			return nil, err
		}
		ids = append(ids, found...)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func unreferencedDepletionRows(tx *gorm.DB, rows []depletionCandidate) ([]depletionCandidate, error) {
	emails := make([]string, 0, len(rows))
	for _, row := range rows {
		emails = append(emails, row.Email)
	}
	stillReferenced := make(map[string]bool)
	field := database.JSONFieldText("client.value", "email")
	query := fmt.Sprintf("SELECT DISTINCT %s %s WHERE %s IN ?", field, database.JSONClientsFromInbound(), field)
	for _, part := range chunkStrings(emails, sqlInChunk) {
		var found []string
		if err := tx.Raw(query, part).Scan(&found).Error; err != nil {
			return nil, err
		}
		for _, email := range found {
			stillReferenced[email] = true
		}
	}
	return slices.DeleteFunc(rows, func(row depletionCandidate) bool { return stillReferenced[row.Email] }), nil
}

func deleteDepletedRecordsTx(tx *gorm.DB, rows []depletionCandidate) error {
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ClientID)
	}
	for _, part := range chunkInts(ids, sqlInChunk) {
		var subIDs []string
		if err := tx.Model(&model.ClientRecord{}).Where("id IN ?", part).Pluck("sub_id", &subIDs).Error; err != nil {
			return err
		}
		if err := clearClientHwidsBySubIDTx(tx, subIDs...); err != nil {
			return err
		}
		for _, model := range []any{&model.ClientInbound{}, &model.ClientExternalLink{}} {
			if err := tx.Where("client_id IN ?", part).Delete(model).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("id IN ?", part).Delete(&model.ClientRecord{}).Error; err != nil {
			return err
		}
	}
	return nil
}
