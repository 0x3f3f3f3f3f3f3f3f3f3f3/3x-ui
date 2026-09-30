package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// References must remain visible when another credential field is malformed.
// This only identifies affected graphs; the normal parser still validates them.
func passwordProxyOwnerReferences(inbound *model.Inbound) []string {
	var settings map[string]json.RawMessage
	if json.Unmarshal([]byte(inbound.Settings), &settings) != nil {
		return nil
	}
	var owners []string
	for field, raw := range settings {
		if !strings.EqualFold(field, "accounts") && !strings.EqualFold(field, "users") {
			continue
		}
		var entries []json.RawMessage
		if json.Unmarshal(raw, &entries) != nil {
			entries = []json.RawMessage{raw}
		}
		for _, entry := range entries {
			var account map[string]json.RawMessage
			if json.Unmarshal(entry, &account) != nil {
				continue
			}
			for key, value := range account {
				if !strings.EqualFold(key, "ownerClientId") {
					continue
				}
				var owner string
				if json.Unmarshal(value, &owner) == nil && owner != "" {
					owners = append(owners, owner)
				}
			}
		}
	}
	return owners
}

// Final cleanup uses email and subscription fields as well as the canonical ID.
// Fence the captured values under the canonical row locks before using them.
func guardClientDeletionSnapshots(tx *gorm.DB, expected []*model.ClientRecord) error {
	ids := make([]int, 0, len(expected))
	for _, record := range expected {
		ids = append(ids, record.Id)
	}
	current, err := lockClientPolicyDeletionRecords(tx, ids)
	if err != nil {
		return err
	}
	byID := make(map[int]model.ClientRecord, len(current))
	for _, record := range current {
		byID[record.Id] = record
	}
	for _, before := range expected {
		after, exists := byID[before.Id]
		if !exists || before.StableID != after.StableID || before.Email != after.Email || before.SubID != after.SubID {
			return fmt.Errorf("%w: client deletion snapshot changed; retry deletion", ErrManagedConfigStale)
		}
	}
	return nil
}

// Inspect both credentials and links: relying only on links would hide an
// account whose saved membership is missing. Unrelated legacy clients retain
// their existing behavior, and bulk callers can report each rejected record.
func passwordProxyRemovalPreflight(tx *gorm.DB, records []*model.ClientRecord) (map[int]error, error) {
	failed := make(map[int]error)
	if len(records) == 0 {
		return failed, nil
	}
	byUUID := make(map[string]int, len(records))
	ids := make([]int, 0, len(records))
	for _, record := range records {
		byUUID[record.StableID] = record.Id
		ids = append(ids, record.Id)
	}
	linked := make(map[int][]int)
	for _, batch := range chunkInts(ids, 400) {
		var links []model.ClientInbound
		if err := tx.Where("client_id IN ?", batch).Find(&links).Error; err != nil {
			return nil, err
		}
		for _, link := range links {
			linked[link.InboundId] = append(linked[link.InboundId], link.ClientId)
		}
	}
	var inbounds []model.Inbound
	if err := tx.Where("protocol IN ?", []model.Protocol{model.Mixed, model.HTTP}).Order("id").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		affected := make(map[int]bool)
		for _, id := range linked[inbound.Id] {
			affected[id] = true
		}
		ownerUUIDs := passwordProxyOwnerReferences(&inbound)
		for _, owner := range ownerUUIDs {
			if id, selected := byUUID[owner]; selected {
				affected[id] = true
			}
		}
		if len(affected) == 0 {
			continue
		}
		_, _, err := passwordProxyAccounts(&inbound)
		if err != nil {
			err = fmt.Errorf("%w: malformed credentials: %w", ErrPasswordProxyOwner, err)
		}
		if err == nil {
			err = validatePasswordProxyOwnerBindings(tx, &inbound)
		}
		if err == nil {
			err = validateLocalClientPolicyResetScope(tx, ownerUUIDs)
		}
		if err != nil {
			for id := range affected {
				failed[id] = fmt.Errorf("password listener %d: %w", inbound.Id, err)
			}
		}
	}
	return failed, nil
}

// Lock the canonical records before checking aliases. A normal ownership writer
// must resolve and lock the same record, so it cannot add a reference between
// this check and the final record deletion. Late references are retried through
// the ordinary per-resource removal path, including its runtime acknowledgement.
func guardPasswordProxyOwnerDeletion(tx *gorm.DB, recordIDs []int) error {
	records, err := lockClientPolicyDeletionRecords(tx, recordIDs)
	if err != nil || len(records) == 0 {
		return err
	}
	selected := make(map[string]bool, len(records))
	pointers := make([]*model.ClientRecord, 0, len(records))
	for i := range records {
		selected[records[i].StableID] = true
		pointers = append(pointers, &records[i])
	}
	failed, err := passwordProxyRemovalPreflight(tx, pointers)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := failed[record.Id]; err != nil {
			return err
		}
	}
	var inbounds []model.Inbound
	if err := tx.Where("protocol IN ?", []model.Protocol{model.Mixed, model.HTTP}).Order("id").Find(&inbounds).Error; err != nil {
		return err
	}
	for _, inbound := range inbounds {
		for _, owner := range passwordProxyOwnerReferences(&inbound) {
			if selected[owner] {
				return fmt.Errorf("%w: listener %d still references selected owner; retry deletion", ErrPasswordProxyOwner, inbound.Id)
			}
		}
	}
	return nil
}

// The caller holds the inbound lock. Read again under the SQL row lock: account
// edits are whole lists, so rebasing an old list can overwrite a rotated alias.
func (s *ClientService) removePasswordProxyOwners(inboundSvc *InboundService, inbound *model.Inbound, records []*model.ClientRecord) (bool, error) {
	wanted := make(map[string]int, len(records))
	for _, record := range records {
		if record != nil {
			wanted[record.StableID] = record.Id
		}
	}
	if len(wanted) == 0 {
		return false, nil
	}
	var saved model.Inbound
	changed := false
	err := runSerializedTx(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&saved, inbound.Id).Error; err != nil {
			return err
		}
		if !isPasswordProxy(saved.Protocol) || saved.NodeID != nil {
			return fmt.Errorf("%w: removal requires a local password listener", ErrPasswordProxyOwner)
		}
		owners, err := resolvePasswordProxyOwners(tx, &saved)
		if err != nil {
			return err
		}
		for _, owner := range owners {
			if id, selected := wanted[owner.StableID]; selected && id != owner.Id {
				return fmt.Errorf("%w: selected canonical identity changed", ErrPasswordProxyOwner)
			}
		}
		settings, _, err := passwordProxyAccounts(&saved)
		if err != nil {
			return err
		}
		var accounts []map[string]json.RawMessage
		if raw := settings["accounts"]; len(raw) != 0 {
			if err := json.Unmarshal(raw, &accounts); err != nil {
				return err
			}
		}
		kept := make([]map[string]json.RawMessage, 0, len(accounts))
		for _, account := range accounts {
			var ownerID string
			if raw := account["ownerClientId"]; len(raw) != 0 {
				if err := json.Unmarshal(raw, &ownerID); err != nil {
					return err
				}
			}
			if _, remove := wanted[ownerID]; remove {
				changed = true
				continue
			}
			kept = append(kept, account)
		}
		if !changed {
			return nil
		}
		settings["accounts"], err = json.Marshal(kept)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		saved.Settings = string(raw)
		if err := preparePasswordProxyOwnerCommand(&saved); err != nil {
			return err
		}
		remaining, err := resolvePasswordProxyOwners(tx, &saved)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.Inbound{}).Where("id = ?", saved.Id).Update("settings", saved.Settings).Error; err != nil {
			return err
		}
		return s.syncPasswordProxyOwnerLinks(tx, &saved, remaining)
	})
	if err != nil || !changed {
		return false, err
	}
	if handled, err := inboundSvc.reconcileManagedChange(&saved); handled {
		return err != nil, err
	}
	// Owned credentials cannot be applied through the legacy email user API.
	return true, nil
}
