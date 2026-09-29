package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Persist the enabled configuration with the reset so restart recovery needs no
// second database mutation and cannot replay an old enable over a newer edit.
func enableLegacyResetClients(tx *gorm.DB, records []model.ClientRecord) (map[int][]string, error) {
	byID := make(map[int]model.ClientRecord)
	var ids []int
	for _, record := range records {
		if !record.Enable {
			ids = append(ids, record.Id)
			byID[record.Id] = record
		}
	}
	wanted := make(map[int]map[string]string)
	now := time.Now().UnixMilli()
	for _, batch := range chunkInts(ids, sqlInChunk) {
		if err := tx.Model(&model.ClientRecord{}).Where("id IN ?", batch).Updates(map[string]any{"enable": true, "updated_at": now}).Error; err != nil {
			return nil, err
		}
		var links []model.ClientInbound
		if err := tx.Where("client_id IN ?", batch).Find(&links).Error; err != nil {
			return nil, err
		}
		for _, link := range links {
			if wanted[link.InboundId] == nil {
				wanted[link.InboundId] = make(map[string]string)
			}
			record := byID[link.ClientId]
			wanted[link.InboundId][record.Email] = record.StableID
		}
	}
	inboundIDs := make([]int, 0, len(wanted))
	for id := range wanted {
		inboundIDs = append(inboundIDs, id)
	}
	slices.Sort(inboundIDs)
	changed := make(map[int][]string)
	for _, batch := range chunkInts(inboundIDs, sqlInChunk) {
		var inbounds []model.Inbound
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", batch).Order("id").Find(&inbounds).Error; err != nil {
			return nil, err
		}
		for _, inbound := range inbounds {
			if err := enableResetInboundClients(tx, &inbound, wanted[inbound.Id], now); err != nil {
				return nil, err
			}
			for _, id := range wanted[inbound.Id] {
				changed[inbound.Id] = append(changed[inbound.Id], id)
			}
		}
	}
	return changed, nil
}

func enableResetInboundClients(tx *gorm.DB, inbound *model.Inbound, wanted map[string]string, now int64) error {
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return err
	}
	var clients []map[string]json.RawMessage
	if err := json.Unmarshal(settings["clients"], &clients); err != nil {
		return err
	}
	found := make(map[string]bool)
	stamp, _ := json.Marshal(now)
	for _, client := range clients {
		var email string
		if err := json.Unmarshal(client["email"], &email); err != nil {
			return err
		}
		if _, ok := wanted[email]; ok {
			client["enable"], client["updated_at"] = json.RawMessage("true"), stamp
			found[email] = true
		}
	}
	if len(found) != len(wanted) {
		return errors.New("reset client missing from inbound settings")
	}
	settings["clients"], _ = json.Marshal(clients)
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := tx.Model(inbound).Update("settings", string(raw)).Error; err != nil {
		return err
	}
	if inbound.NodeID != nil {
		return (&NodeService{}).MarkNodeDirtyTx(tx, *inbound.NodeID)
	}
	return nil
}

func applyLegacyResetEnable(ctx context.Context, inboundSvc *InboundService, inboundID int, ids []string) error {
	defer lockInbound(inboundID).Unlock()
	inbound, err := inboundSvc.GetInbound(inboundID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil || !inbound.Enable {
		return err
	}
	var records []model.ClientRecord
	for _, batch := range chunkStrings(ids, sqlInChunk) {
		var found []model.ClientRecord
		if err := database.GetDB().Table("clients c").Select("c.*").
			Joins("JOIN client_inbounds ci ON ci.client_id = c.id").
			Where("ci.inbound_id = ? AND c.stable_id IN ? AND c.enable = ?", inboundID, batch, true).Find(&found).Error; err != nil {
			return err
		}
		records = append(records, found...)
	}
	managed, err := managedClientResetIDs(database.GetDB(), records)
	if err != nil {
		return err
	}
	wanted := make(map[string]bool)
	for _, record := range records {
		if _, ok := slices.BinarySearch(managed, record.StableID); !ok {
			wanted[record.Email] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	rt, push, _, err := inboundSvc.nodePushPlan(inbound)
	if err != nil || !push {
		return err
	}
	if inbound.NodeID == nil {
		switch inbound.Protocol {
		case model.MTProto:
			inboundSvc.applyLocalMtproto(inboundID)
			return nil
		case model.AmneziaWG:
			inboundSvc.applyLocalAmneziaWG(inboundID)
			return nil
		case model.TUIC:
			inboundSvc.applyLocalTuic(inboundID)
			return nil
		}
	} else if len(wanted) > nodeBulkPushThreshold {
		return nil
	}
	clients, err := inboundSvc.GetClients(inbound)
	if err != nil {
		return err
	}
	var settings struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return err
	}
	for _, client := range clients {
		if !wanted[client.Email] || !client.Enable {
			continue
		}
		if inbound.NodeID != nil {
			pushCtx, cancel := context.WithTimeout(ctx, nodeClientPushTimeout)
			err = rt.UpdateUser(pushCtx, inbound, client.Email, client)
			cancel()
		} else {
			err = rt.AddUser(ctx, inbound, map[string]any{
				"email": client.Email, "id": client.ID, "security": client.Security,
				"flow": client.Flow, "auth": client.Auth, "password": client.Password,
				"cipher": settings.Method, "reverse": client.Reverse,
				"publicKey": client.PublicKey, "allowedIPs": client.AllowedIPs,
				"preSharedKey": client.PreSharedKey, "keepAlive": keepAliveStr(client.KeepAliveSeconds()),
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}
