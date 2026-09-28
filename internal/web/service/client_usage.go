package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const legacyUsageOnly = `NOT EXISTS (
	SELECT 1 FROM clients c JOIN client_usage_accounts a ON a.policy_id = c.policy_id
	WHERE c.email = client_traffics.email
)`

func clientHasUsageAccount(tx *gorm.DB, email string) (bool, error) {
	var count int64
	err := tx.Model(&model.ClientUsageAccount{}).
		Joins("JOIN clients ON clients.policy_id = client_usage_accounts.policy_id").
		Where("clients.email = ?", email).Count(&count).Error
	return count != 0, err
}

// Canonical rows serialize activation, admission and reset before either counter representation changes.
func resetClientUsageTx(tx *gorm.DB, emails []string) (int64, error) {
	var affected int64
	emails = trimmedUniqueEmails(emails)
	var clients []model.ClientRecord
	for _, batch := range chunkStrings(emails, sqlInChunk) {
		var rows []model.ClientRecord
		if err := tx.Where("email IN ?", batch).Find(&rows).Error; err != nil {
			return 0, err
		}
		clients = append(clients, rows...)
	}
	slices.SortFunc(clients, func(a, b model.ClientRecord) int { return a.Id - b.Id })
	ids := make([]int, len(clients))
	for i, client := range clients {
		ids[i] = client.Id
	}
	managed := make(map[string]bool)
	for _, part := range chunkInts(ids, sqlInChunk) {
		if err := lockUsageClientsTx(tx, part); err != nil {
			return 0, err
		}
		var owners []string
		if err := tx.Model(&model.ClientUsageAccount{}).
			Joins("JOIN clients ON clients.policy_id = client_usage_accounts.policy_id").
			Where("clients.id IN ?", part).Pluck("client_usage_accounts.policy_id", &owners).Error; err != nil {
			return 0, err
		}
		for _, owner := range owners {
			managed[owner] = true
		}
	}
	if err := adjustGroupBaselinesForRemovedTraffic(tx, emails); err != nil {
		return 0, err
	}
	for _, client := range clients {
		if managed[client.PolicyID] {
			if _, err := database.NewClientUsageLedger(tx).ResetAdmitted(context.Background(), client.PolicyID); err != nil {
				return 0, err
			}
			affected++
		}
	}
	for _, batch := range chunkStrings(emails, sqlInChunk) {
		res := tx.Model(&xray.ClientTraffic{}).Where("email IN ?", batch).Where(legacyUsageOnly).
			Updates(map[string]any{"enable": true, "up": 0, "down": 0})
		if res.Error != nil {
			return 0, res.Error
		}
		affected += res.RowsAffected
	}
	return affected, nil
}

// IDs arrive sorted across all chunks; PostgreSQL takes row locks in that same order.
func lockUsageClientsTx(tx *gorm.DB, ids []int) error {
	if tx.Name() == "sqlite" {
		res := tx.Model(&model.ClientRecord{}).Where("id IN ?", ids).UpdateColumn("id", gorm.Expr("id"))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != int64(len(ids)) {
			return gorm.ErrRecordNotFound
		}
		return nil
	}
	var locked []int
	if err := tx.Model(&model.ClientRecord{}).Where("id IN ?", ids).Order("id").
		Clauses(clause.Locking{Strength: "UPDATE"}).Pluck("id", &locked).Error; err != nil {
		return err
	}
	if len(locked) != len(ids) {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func lockUsageClientTx(tx *gorm.DB, id int) error {
	locked := tx.Model(&model.ClientRecord{}).Where("id = ?", id).UpdateColumn("id", gorm.Expr("id"))
	if locked.Error != nil {
		return locked.Error
	}
	if locked.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *InboundService) resetManagedClientTrafficLocked(id int, email string) (bool, *model.Inbound, error) {
	inbound, err := s.GetInbound(id)
	if err != nil {
		return false, nil, err
	}
	return false, inbound, resetManagedClientTraffic([]int{id}, email)
}

func resetManagedClientTraffic(ids []int, email string) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		if _, err := resetClientUsageTx(tx, []string{email}); err != nil {
			return err
		}
		if err := clearGlobalTraffic(tx, email); err != nil {
			return err
		}
		if err := tx.Where("email = ?", email).Delete(&model.NodeClientTraffic{}).Error; err != nil {
			return err
		}
		seen := make(map[int]bool)
		for _, part := range chunkInts(ids, sqlInChunk) {
			if err := tx.Model(&model.Inbound{}).Where("id IN ?", part).Update("last_traffic_reset_time", time.Now().UnixMilli()).Error; err != nil {
				return err
			}
			var nodes []int
			if err := tx.Model(&model.Inbound{}).Where("id IN ? AND node_id IS NOT NULL", part).Distinct("node_id").Pluck("node_id", &nodes).Error; err != nil {
				return err
			}
			for _, node := range nodes {
				if !seen[node] {
					if err := (&NodeService{}).MarkNodeDirtyTx(tx, node); err != nil {
						return err
					}
					seen[node] = true
				}
			}
		}
		return nil
	})
}

func (s *InboundService) renewManagedClientsTx(tx *gorm.DB, batch *trafficMutationBatch) (int64, error) {
	now := time.Now().UnixMilli()
	var clients []model.ClientRecord
	err := tx.Model(&model.ClientRecord{}).
		Joins("JOIN client_usage_accounts a ON a.policy_id = clients.policy_id").
		Joins("JOIN client_traffics t ON t.email = clients.email AND t.policy_id = clients.policy_id").
		Where("(t.reset > 0 OR t.reset_day > 0 OR t.reset_weekday > 0) AND t.expiry_time > 0 AND t.expiry_time <= ?", now).
		Where("t.reset_max <= 0 OR t.reset_count < t.reset_max").
		Where("EXISTS (SELECT 1 FROM client_inbounds ci JOIN inbounds i ON i.id = ci.inbound_id WHERE ci.client_id = clients.id AND i.node_id IS NULL)").
		Order("clients.id").Find(&clients).Error
	if err != nil || len(clients) == 0 {
		return 0, err
	}
	loc, err := (&SettingService{}).GetTimeLocation()
	if err != nil || loc == nil {
		loc = time.UTC
	}
	var count int64
	for _, client := range clients {
		if err := lockUsageClientTx(tx, client.Id); err != nil {
			return 0, err
		}
	}
	for _, client := range clients {
		var traffic xray.ClientTraffic
		if err := tx.Where("email = ? AND policy_id = ?", client.Email, client.PolicyID).First(&traffic).Error; err != nil {
			return 0, err
		}
		expiry, renewals := catchUpClientRenewal(&traffic, now, loc)
		if expiry == traffic.ExpiryTime && renewals == 0 {
			continue
		}
		if renewals > 0 && expiry > now {
			if _, err := resetClientUsageTx(tx, []string{client.Email}); err != nil {
				return 0, err
			}
			if err := clearGlobalTraffic(tx, client.Email); err != nil {
				return 0, err
			}
			if err := tx.Where("email = ?", client.Email).Delete(&model.NodeClientTraffic{}).Error; err != nil {
				return 0, err
			}
			count++
		}
		if err := tx.Model(&traffic).Updates(map[string]any{"expiry_time": expiry, "reset_count": traffic.ResetCount + renewals}).Error; err != nil {
			return 0, err
		}
		if err := tx.Model(&model.ClientRecord{}).Where("id = ?", client.Id).Updates(map[string]any{"expiry_time": expiry, "updated_at": now}).Error; err != nil {
			return 0, err
		}
		if err := updateManagedExpirySettingsTx(tx, client, expiry, batch); err != nil {
			return 0, err
		}
	}
	return count, nil
}

func updateManagedExpirySettingsTx(tx *gorm.DB, client model.ClientRecord, expiry int64, batch *trafficMutationBatch) error {
	var inbounds []model.Inbound
	if err := tx.Model(&model.Inbound{}).
		Joins("JOIN client_inbounds ci ON ci.inbound_id = inbounds.id").
		Where("ci.client_id = ?", client.Id).Order("inbounds.id").Find(&inbounds).Error; err != nil {
		return err
	}
	for _, inbound := range inbounds {
		var settings map[string]any
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			return err
		}
		entries, _ := settings["clients"].([]any)
		for _, entry := range entries {
			if fields, ok := entry.(map[string]any); ok && fields["email"] == client.Email {
				fields["expiryTime"] = expiry
			}
		}
		encoded, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("settings", string(encoded)).Error; err != nil {
			return err
		}
		if inbound.NodeID != nil {
			batch.addNode(*inbound.NodeID)
		}
	}
	return nil
}

// Node calls run outside the traffic writer and use one client reset per node, regardless of attachments.
func (s *ClientService) propagateManagedClientReset(inboundSvc *InboundService, ids []int, email string) error {
	seen := make(map[int]bool)
	var failures []error
	for _, id := range ids {
		inbound, err := inboundSvc.GetInbound(id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if inbound.NodeID == nil || seen[*inbound.NodeID] {
			continue
		}
		seen[*inbound.NodeID] = true
		rt, err := inboundSvc.runtimeFor(inbound)
		if err == nil {
			ctx, cancel := nodePushContext()
			err = rt.ResetClientTraffic(ctx, inbound, email)
			cancel()
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("local reset committed, node %d reset failed: %w", *inbound.NodeID, err))
		}
	}
	return errors.Join(failures...)
}
