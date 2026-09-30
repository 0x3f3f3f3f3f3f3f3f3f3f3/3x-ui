package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var ErrTunnelOwnerConflict = errors.New("Tunnel listener can belong to only one client")

func prepareTunnelOwnerCommand(inbound *model.Inbound) error {
	if inbound.OwnerClientID == nil {
		return nil
	}
	if inbound.Protocol != model.Tunnel || inbound.NodeID != nil {
		return errors.New("ownerClientId requires a local Tunnel listener")
	}
	if _, err := uuid.Parse(*inbound.OwnerClientID); err != nil {
		return errors.New("ownerClientId requires an existing stable client UUID")
	}
	if len(inbound.ClientStats) != 0 {
		return errors.New("ownerClientId cannot be combined with clientStats")
	}
	return setTunnelOwnerClients(inbound, nil)
}

func setTunnelOwnerClients(inbound *model.Inbound, owner *model.ClientRecord) error {
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return err
	}
	if settings == nil {
		return errors.New("Tunnel settings must be an object")
	}
	clients := []model.Client{}
	if owner != nil {
		clients = append(clients, *owner.ToClient())
	}
	raw, err := json.Marshal(clients)
	if err != nil {
		return err
	}
	settings["clients"] = raw
	raw, err = json.Marshal(settings)
	if err == nil {
		inbound.Settings = string(raw)
	}
	return err
}

func resolveTunnelOwnerCommand(tx *gorm.DB, inbound *model.Inbound) (*model.ClientRecord, error) {
	if inbound.OwnerClientID == nil {
		return nil, nil
	}
	if inbound.Id != 0 {
		if _, err := validateTunnelOwnerLinks(tx, inbound.Id, nil, nil, false); err != nil {
			return nil, err
		}
	}
	var owner model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id = ?", *inbound.OwnerClientID).First(&owner).Error; err != nil {
		return nil, fmt.Errorf("resolve Tunnel owner: %w", err)
	}
	if err := validateLocalClientPolicyResetScope(tx, []string{owner.StableID}); err != nil {
		return nil, fmt.Errorf("Tunnel owner must have only local memberships: %w", err)
	}
	if err := setTunnelOwnerClients(inbound, &owner); err != nil {
		return nil, err
	}
	return &owner, nil
}

func (s *ClientService) syncTunnelOwnerLink(tx *gorm.DB, inboundID int, owner *model.ClientRecord) error {
	if _, err := validateTunnelOwnerLinks(tx, inboundID, []model.Client{*owner.ToClient()}, nil, true); err != nil {
		return err
	}
	if err := s.reconcileInboundLinks(tx, inboundID, map[int]string{owner.Id: ""}, []int{owner.Id}, nil, true); err != nil {
		return err
	}
	return validateStoredTunnelSourceACLOwner(tx, inboundID)
}

func preserveDetachedTunnelOwnerTraffic(tx *gorm.DB, inboundID int, email string) (bool, error) {
	var owner model.ClientRecord
	if err := tx.Select("id").Where("email = ?", email).Find(&owner).Error; err != nil {
		return false, err
	}
	if owner.Id == 0 {
		return false, nil
	}
	var sibling model.ClientInbound
	if err := tx.Where("client_id = ? AND inbound_id <> ?", owner.Id, inboundID).
		Order("inbound_id").Limit(1).Find(&sibling).Error; err != nil {
		return false, err
	}
	err := tx.Model(&xray.ClientTraffic{}).Where("email = ? AND inbound_id = ?", email, inboundID).
		Update("inbound_id", sibling.InboundId).Error
	return true, err
}

// Final deletion rechecks canonical memberships created after the fanout snapshot.
func deleteClientLinksAndDisableTunnels(tx *gorm.DB, clientIDs []int) (bool, error) {
	candidates := make(map[int]struct{})
	for _, batch := range chunkInts(clientIDs, sqlInChunk) {
		var ids []int
		if err := tx.Table("inbounds i").Select("i.id").Joins("JOIN client_inbounds ci ON ci.inbound_id = i.id").
			Where("i.protocol = ? AND ci.client_id IN ?", model.Tunnel, batch).Pluck("i.id", &ids).Error; err != nil {
			return false, err
		}
		for _, id := range ids {
			candidates[id] = struct{}{}
		}
	}
	ids := make([]int, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var tunnels []model.Inbound
	for _, batch := range chunkInts(ids, sqlInChunk) {
		var rows []model.Inbound
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "enable", "node_id").
			Where("id IN ? AND protocol = ?", batch, model.Tunnel).Order("id").Find(&rows).Error; err != nil {
			return false, err
		}
		tunnels = append(tunnels, rows...)
	}
	for _, batch := range chunkInts(clientIDs, sqlInChunk) {
		if err := tx.Where("client_id IN ?", batch).Delete(&model.ClientInbound{}).Error; err != nil {
			return false, err
		}
	}
	localChanged := false
	for _, tunnel := range tunnels {
		linked := tx.Model(&model.ClientInbound{}).Select("1").Where("inbound_id = ?", tunnel.Id)
		changed := tx.Model(&model.Inbound{}).Where("id = ? AND enable = ? AND NOT EXISTS (?)", tunnel.Id, true, linked).Update("enable", false)
		if changed.Error != nil {
			return false, changed.Error
		}
		if changed.RowsAffected == 0 {
			continue
		}
		if tunnel.NodeID == nil {
			localChanged = true
		} else if err := (&NodeService{}).MarkNodeDirtyTx(tx, *tunnel.NodeID); err != nil {
			return false, err
		}
	}
	return localChanged, nil
}

func annotateTunnelOwners(db *gorm.DB, inbounds []*model.Inbound) error {
	byID := make(map[int]*model.Inbound)
	var ids []int
	for _, inbound := range inbounds {
		inbound.OwnerClientID = nil
		if inbound.Protocol == model.Tunnel {
			byID[inbound.Id] = inbound
			ids = append(ids, inbound.Id)
		}
	}
	for _, batch := range chunkInts(ids, 400) {
		var links []struct {
			InboundID int
			StableID  string
		}
		if err := db.Table("client_inbounds ci").Select("ci.inbound_id, c.stable_id").
			Joins("JOIN clients c ON c.id = ci.client_id").Where("ci.inbound_id IN ?", batch).Scan(&links).Error; err != nil {
			return err
		}
		for _, link := range links {
			inbound := byID[link.InboundID]
			if inbound.OwnerClientID != nil {
				return ErrTunnelOwnerConflict
			}
			inbound.OwnerClientID = &link.StableID
		}
	}
	return nil
}

func validateTunnelOwnerLinks(tx *gorm.DB, inboundID int, clients []model.Client, detachEmails []string, prune bool) (bool, error) {
	var inbound model.Inbound
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
		Where("id = ? AND protocol = ?", inboundID, model.Tunnel).Find(&inbound).Error; err != nil {
		return false, err
	}
	if inbound.Id == 0 {
		return false, nil
	}
	var emails []string
	if err := tx.Table("clients c").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").
		Where("ci.inbound_id = ?", inboundID).Pluck("c.email", &emails).Error; err != nil {
		return false, err
	}
	owners := make(map[string]bool)
	if !prune {
		for _, email := range emails {
			owners[strings.ToLower(strings.TrimSpace(email))] = true
		}
	}
	for _, email := range detachEmails {
		delete(owners, strings.ToLower(strings.TrimSpace(email)))
	}
	for _, client := range clients {
		email := strings.ToLower(strings.TrimSpace(client.Email))
		if email != "" {
			owners[email] = true
		}
	}
	if len(owners) > 1 {
		return false, ErrTunnelOwnerConflict
	}
	return len(emails) != 0 && len(owners) == 0, nil
}

func stopOwnedTunnelListener(s *InboundService, inbound *model.Inbound) (bool, error) {
	if process := currentXrayProcess(); process == nil || !process.IsRunning() {
		return true, nil
	}
	rt, err := s.runtimeFor(inbound)
	if err != nil {
		return true, err
	}
	err = rt.DelInbound(context.Background(), inbound)
	return err != nil, err
}
