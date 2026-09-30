package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func passwordProxyFieldOwner(tx *gorm.DB, record *model.ClientRecord) (bool, error) {
	failed, err := passwordProxyRemovalPreflight(tx, []*model.ClientRecord{record})
	if err != nil {
		return false, err
	}
	if err := failed[record.Id]; err != nil {
		return false, err
	}
	var count int64
	if err := tx.Model(&model.ClientInbound{}).Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
		Where("client_inbounds.client_id = ? AND inbounds.protocol IN ?", record.Id, []model.Protocol{model.Mixed, model.HTTP}).Count(&count).Error; err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	return true, guardPasswordProxyOwnerUpdateScope(tx, record, nil)
}

type passwordOwnerFieldSelection struct {
	Record *model.ClientRecord
	Links  []model.ClientInbound
	Owned  bool
}

func capturePasswordOwnerFieldSelections(tx *gorm.DB, records []*model.ClientRecord) (map[int]*passwordOwnerFieldSelection, error) {
	selections := make(map[int]*passwordOwnerFieldSelection, len(records))
	var ids []int
	for _, record := range records {
		ids = append(ids, record.Id)
		selections[record.Id] = &passwordOwnerFieldSelection{Record: record}
	}
	links, err := passwordOwnerFieldLinks(tx, ids)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		selections[link.ClientId].Links = append(selections[link.ClientId].Links, link)
	}
	return selections, nil
}

// Classification is a captured selection, never permission to re-resolve a
// mutable label. The same fence runs inside each legacy writer transaction.
func guardPasswordOwnerFieldSelections(tx *gorm.DB, selections map[int]*passwordOwnerFieldSelection, lockRows bool) error {
	if len(selections) == 0 {
		return nil
	}
	var ids []int
	var stableIDs []string
	for id, selection := range selections {
		ids = append(ids, id)
		stableIDs = append(stableIDs, selection.Record.StableID)
	}
	sort.Strings(stableIDs)
	var current []*model.ClientRecord
	for _, batch := range chunkStrings(stableIDs, sqlInChunk) {
		query := tx
		if lockRows {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var rows []*model.ClientRecord
		if err := query.Where("stable_id IN ?", batch).Order("stable_id").Find(&rows).Error; err != nil {
			return err
		}
		current = append(current, rows...)
	}
	if len(current) != len(selections) {
		return ErrManagedConfigStale
	}
	for _, record := range current {
		selected := selections[record.Id]
		if selected == nil || record.Email != selected.Record.Email || record.SubID != selected.Record.SubID || record.StableID != selected.Record.StableID {
			return ErrManagedConfigStale
		}
	}
	links, err := passwordOwnerFieldLinks(tx, ids)
	if err != nil {
		return err
	}
	byID := make(map[int][]model.ClientInbound)
	for _, link := range links {
		byID[link.ClientId] = append(byID[link.ClientId], link)
	}
	for id, selected := range selections {
		if !reflect.DeepEqual(selected.Links, byID[id]) {
			return fmt.Errorf("%w: field selection memberships changed", ErrManagedConfigStale)
		}
	}
	failed, err := passwordProxyRemovalPreflight(tx, current)
	if err != nil {
		return err
	}
	owned := make(map[int]bool)
	for _, batch := range chunkInts(ids, sqlInChunk) {
		var selected []int
		if err := tx.Model(&model.ClientInbound{}).Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
			Where("client_inbounds.client_id IN ? AND inbounds.protocol IN ?", batch, []model.Protocol{model.Mixed, model.HTTP}).
			Distinct("client_inbounds.client_id").Pluck("client_inbounds.client_id", &selected).Error; err != nil {
			return err
		}
		for _, id := range selected {
			owned[id] = true
		}
	}
	for _, record := range current {
		if err := failed[record.Id]; err != nil {
			return err
		}
		if selections[record.Id].Owned != owned[record.Id] {
			return fmt.Errorf("%w: field selection ownership changed", ErrManagedConfigStale)
		}
	}
	return nil
}

func (s *ClientService) passwordProxyFieldSelectionByEmail(email string) (*passwordOwnerFieldSelection, error) {
	current, err := s.GetRecordByEmail(nil, email)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	selections, err := capturePasswordOwnerFieldSelections(database.GetDB(), []*model.ClientRecord{current})
	if err != nil {
		return nil, err
	}
	selected := selections[current.Id]
	selected.Owned, err = passwordProxyFieldOwner(database.GetDB(), current)
	if err != nil {
		return nil, err
	}
	if err := guardPasswordOwnerFieldSelections(database.GetDB(), selections, false); err != nil {
		return nil, err
	}
	return selected, nil
}

func passwordOwnerFieldValue(current *model.ClientRecord, field string, requested any) (value any, column, statColumn string, changed bool, err error) {
	switch field {
	case "enable":
		next := !current.Enable
		if requested != nil {
			var ok bool
			next, ok = requested.(bool)
			if !ok {
				return nil, "", "", false, errors.New("enable must be boolean")
			}
		}
		return next, "enable", "enable", next != current.Enable, nil
	case "limitIp":
		next, ok := requested.(int)
		if !ok || next < 0 {
			return nil, "", "", false, errors.New("limitIp must be a nonnegative integer")
		}
		return next, "limit_ip", "", next != current.LimitIP, nil
	case "expiryTime":
		next, ok := requested.(int64)
		if !ok {
			return nil, "", "", false, errors.New("expiryTime must be an integer")
		}
		return next, "expiry_time", "expiry_time", next != current.ExpiryTime, nil
	case "totalGB":
		next, ok := requested.(int64)
		if !ok || next < 0 {
			return nil, "", "", false, errors.New("totalGB must be a nonnegative integer")
		}
		return next, "total_gb", "total", next != current.TotalGB, nil
	default:
		return nil, "", "", false, fmt.Errorf("unsupported shared client field %q", field)
	}
}

func passwordOwnerFieldLinks(tx *gorm.DB, ids []int) ([]model.ClientInbound, error) {
	var links []model.ClientInbound
	for _, batch := range chunkInts(ids, sqlInChunk) {
		var rows []model.ClientInbound
		if err := tx.Select("client_id", "inbound_id").Where("client_id IN ?", batch).Find(&rows).Error; err != nil {
			return nil, err
		}
		links = append(links, rows...)
	}
	sort.Slice(links, func(i, j int) bool {
		return links[i].InboundId < links[j].InboundId || links[i].InboundId == links[j].InboundId && links[i].ClientId < links[j].ClientId
	})
	return links, nil
}

type passwordOwnerFieldResult struct {
	Changed bool
	Value   any
}

func (s *ClientService) updatePasswordOwnerFields(inbounds *InboundService, selections []*passwordOwnerFieldSelection, field string, requested any) (map[int]passwordOwnerFieldResult, bool, error) {
	expected := make([]*model.ClientRecord, 0, len(selections))
	var links []model.ClientInbound
	for _, selected := range selections {
		expected = append(expected, selected.Record)
		links = append(links, selected.Links...)
	}
	sort.Slice(links, func(i, j int) bool {
		return links[i].InboundId < links[j].InboundId || links[i].InboundId == links[j].InboundId && links[i].ClientId < links[j].ClientId
	})
	changed := make(map[int]passwordOwnerFieldResult, len(expected))
	if len(expected) == 0 {
		return changed, false, nil
	}
	ids := make([]int, 0, len(expected))
	byID := make(map[int]*model.ClientRecord, len(expected))
	for _, record := range expected {
		ids = append(ids, record.Id)
		byID[record.Id] = record
	}
	resources := make(map[int]bool)
	for _, link := range links {
		resources[link.InboundId] = true
	}
	inboundIDs := make([]int, 0, len(resources))
	for id := range resources {
		inboundIDs = append(inboundIDs, id)
	}
	sort.Ints(inboundIDs)
	var held []*sync.Mutex
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].Unlock()
		}
	}()
	for _, id := range inboundIDs {
		held = append(held, lockInbound(id))
	}
	err := runSerializedTx(func(tx *gorm.DB) error {
		var saved []model.Inbound
		for _, batch := range chunkInts(inboundIDs, sqlInChunk) {
			var rows []model.Inbound
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", batch).Order("id").Find(&rows).Error; err != nil {
				return err
			}
			saved = append(saved, rows...)
		}
		if len(saved) != len(inboundIDs) {
			return ErrManagedConfigStale
		}
		stableIDs := make([]string, 0, len(expected))
		for _, record := range expected {
			stableIDs = append(stableIDs, record.StableID)
		}
		sort.Strings(stableIDs)
		var current []model.ClientRecord
		for _, batch := range chunkStrings(stableIDs, sqlInChunk) {
			var rows []model.ClientRecord
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", batch).Order("stable_id").Find(&rows).Error; err != nil {
				return err
			}
			current = append(current, rows...)
		}
		if len(current) != len(expected) {
			return ErrManagedConfigStale
		}
		currentLinks, err := passwordOwnerFieldLinks(tx, ids)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(links, currentLinks) {
			return fmt.Errorf("%w: owner memberships changed during field update", ErrManagedConfigStale)
		}
		currentPointers := make([]*model.ClientRecord, 0, len(current))
		for i := range current {
			currentPointers = append(currentPointers, &current[i])
		}
		failed, err := passwordProxyRemovalPreflight(tx, currentPointers)
		if err != nil {
			return err
		}
		passwordResources := make(map[int]bool)
		for _, inbound := range saved {
			passwordResources[inbound.Id] = isPasswordProxy(inbound.Protocol)
		}
		ownedIDs := make(map[int]bool)
		for _, link := range currentLinks {
			ownedIDs[link.ClientId] = ownedIDs[link.ClientId] || passwordResources[link.InboundId]
		}
		for i := range current {
			record := &current[i]
			prior := byID[record.Id]
			if prior == nil || record.StableID != prior.StableID || record.Email != prior.Email || record.SubID != prior.SubID {
				return ErrManagedConfigStale
			}
			if err := failed[record.Id]; err != nil {
				return err
			}
			if !ownedIDs[record.Id] {
				return ErrManagedConfigStale
			}
			if err := guardPasswordProxyOwnerUpdateScope(tx, record, nil); err != nil {
				return err
			}
		}
		now := time.Now().UnixMilli()
		wireValues := make(map[string]any, len(current))
		for i := range current {
			record := &current[i]
			value, column, statColumn, didChange, err := passwordOwnerFieldValue(record, field, requested)
			if err != nil {
				return err
			}
			changed[record.Id], wireValues[record.Email] = passwordOwnerFieldResult{Changed: didChange, Value: value}, value
			if err := tx.Model(&model.ClientRecord{}).Where("id = ?", record.Id).
				Updates(map[string]any{column: value, "updated_at": now}).Error; err != nil {
				return err
			}
			if statColumn != "" {
				if err := tx.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Update(statColumn, value).Error; err != nil {
					return err
				}
			}
		}
		for i := range saved {
			inbound := &saved[i]
			if isPasswordProxy(inbound.Protocol) {
				continue
			}
			var settings map[string]json.RawMessage
			if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
				return err
			}
			var entries []map[string]json.RawMessage
			if clients := settings["clients"]; len(clients) != 0 {
				if err := json.Unmarshal(clients, &entries); err != nil {
					return err
				}
			}
			if inbound.Protocol == model.Tunnel && len(entries) == 0 {
				continue
			}
			found := make(map[string]bool)
			attached := make(map[string]bool)
			for _, link := range currentLinks {
				if link.InboundId == inbound.Id {
					attached[byID[link.ClientId].Email] = true
				}
			}
			for _, entry := range entries {
				var email string
				if err := json.Unmarshal(entry["email"], &email); err != nil {
					return err
				}
				if value, selected := wireValues[email]; selected && attached[email] {
					entry[field], err = json.Marshal(value)
					if err != nil {
						return err
					}
					entry["updated_at"], err = json.Marshal(now)
					if err != nil {
						return err
					}
					found[email] = true
				}
			}
			for _, link := range currentLinks {
				if link.InboundId == inbound.Id && !found[byID[link.ClientId].Email] {
					return fmt.Errorf("%w: ordinary client mirror is missing", ErrManagedConfigStale)
				}
			}
			settings["clients"], err = json.Marshal(entries)
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(settings)
			if err != nil {
				return err
			}
			if err := tx.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("settings", string(encoded)).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if handled, err := inbounds.reconcileManagedChange(&model.Inbound{Protocol: model.HTTP}); handled {
		return changed, err != nil, err
	}
	return changed, true, nil
}

func (s *ClientService) applySharedClientFieldByEmail(inbounds *InboundService, email, field string, value any) (bool, error) {
	owner, err := s.passwordProxyFieldSelectionByEmail(email)
	if err != nil {
		return false, err
	}
	if owner != nil && owner.Owned {
		_, restart, err := s.updatePasswordOwnerFields(inbounds, []*passwordOwnerFieldSelection{owner}, field, value)
		return restart, err
	}
	return s.applyClientFieldSelectionByEmail(inbounds, email, owner, func(client map[string]any) { client[field] = value })
}

// Remove owned/invalid identities from legacy email fanout before any resource
// writes. Bounded local batches acknowledge one canonical candidate each.
func (s *ClientService) bulkPasswordOwnerEnable(inbounds *InboundService, records map[string]*model.ClientRecord, selections map[int]*passwordOwnerFieldSelection, skipped map[string]string, enable bool) (int, bool, error) {
	var candidates []*model.ClientRecord
	var ids []int
	for _, record := range records {
		candidates = append(candidates, record)
		ids = append(ids, record.Id)
	}
	failed, err := passwordProxyRemovalPreflight(database.GetDB(), candidates)
	if err != nil {
		return 0, false, err
	}
	ownedIDs := make(map[int]bool)
	for _, batch := range chunkInts(ids, sqlInChunk) {
		var selected []int
		if err := database.GetDB().Model(&model.ClientInbound{}).Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
			Where("client_inbounds.client_id IN ? AND inbounds.protocol IN ?", batch, []model.Protocol{model.Mixed, model.HTTP}).
			Distinct("client_inbounds.client_id").Pluck("client_inbounds.client_id", &selected).Error; err != nil {
			return 0, false, err
		}
		for _, id := range selected {
			ownedIDs[id] = true
		}
	}
	validSelections := make(map[int]*passwordOwnerFieldSelection)
	for _, record := range candidates {
		selections[record.Id].Owned = ownedIDs[record.Id]
		if failed[record.Id] == nil {
			validSelections[record.Id] = selections[record.Id]
		}
	}
	if err := guardPasswordOwnerFieldSelections(database.GetDB(), validSelections, false); err != nil {
		return 0, false, err
	}
	var owned []*passwordOwnerFieldSelection
	for _, record := range candidates {
		if err := failed[record.Id]; err != nil {
			skipped[record.Email] = err.Error()
			delete(records, record.Email)
			continue
		}
		if !ownedIDs[record.Id] {
			continue
		}
		delete(records, record.Email)
		if err := guardPasswordProxyOwnerUpdateScope(database.GetDB(), record, nil); err != nil {
			skipped[record.Email] = err.Error()
			continue
		}
		owned = append(owned, selections[record.Id])
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Record.StableID < owned[j].Record.StableID })
	changed, restart := 0, false
	const batchSize = 128
	for start := 0; start < len(owned); start += batchSize {
		batch := owned[start:min(start+batchSize, len(owned))]
		_, needsRestart, err := s.updatePasswordOwnerFields(inbounds, batch, "enable", enable)
		restart = restart || needsRestart
		if err != nil {
			for _, selected := range batch {
				skipped[selected.Record.Email] = err.Error()
			}
			continue
		}
		changed += len(batch)
	}
	return changed, restart, nil
}
