package service

import (
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func guardRemoteClientPolicyAttachments(tx *gorm.DB, inboundID int, existing map[string]*model.ClientRecord, clients []model.Client) error {
	if len(clients) == 0 {
		return nil
	}
	var inbound struct {
		NodeID   *int
		Protocol model.Protocol
	}
	if err := tx.Model(&model.Inbound{}).Select("node_id", "protocol").First(&inbound, inboundID).Error; err != nil {
		return err
	}
	hasPolicy := slices.ContainsFunc(clients, func(client model.Client) bool { return client.Policy != nil })
	for _, record := range existing {
		hasPolicy = hasPolicy || record.Policy != nil || record.DesiredPolicyVersion != 0
	}
	localOwnedResource := inbound.NodeID == nil && (inbound.Protocol == model.Tunnel || inbound.Protocol == model.Mieru || inbound.Protocol == model.SSH || inbound.Protocol == model.Snell || isPasswordProxy(inbound.Protocol))
	if inbound.NodeID == nil && !hasPolicy && !localOwnedResource {
		return nil
	}
	ids := make([]string, 0, len(existing))
	for _, record := range existing {
		ids = append(ids, record.StableID)
	}
	slices.Sort(ids)
	// Match policy preparation's stable-ID lock order, including across batches.
	for _, batch := range chunkStrings(ids, 400) {
		var rows []model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", batch).Order("stable_id").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != len(batch) {
			return ErrManagedConfigStale
		}
		for i := range rows {
			row := &rows[i]
			if inbound.NodeID != nil && row.DesiredPolicyVersion != 0 {
				return remoteClientPolicyScopeError()
			}
			prior := existing[row.Email]
			if prior == nil || prior.StableID != row.StableID {
				return ErrManagedConfigStale
			}
			existing[row.Email] = row
		}
	}
	policyIDs := make([]int, 0, len(existing))
	seen := make(map[int]bool)
	for _, client := range clients {
		record := existing[strings.TrimSpace(client.Email)]
		if record != nil && !seen[record.Id] && (inbound.NodeID != nil || localOwnedResource || client.Policy != nil || record.Policy != nil || record.DesiredPolicyVersion != 0) {
			seen[record.Id] = true
			policyIDs = append(policyIDs, record.Id)
		}
	}
	linked, remote := make(map[int]bool), make(map[int]bool)
	for _, batch := range chunkInts(policyIDs, 400) {
		if inbound.NodeID != nil {
			var localOwners int64
			if err := tx.Model(&model.ClientInbound{}).Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
				Where("client_inbounds.client_id IN ? AND inbounds.node_id IS NULL AND inbounds.protocol IN ?", batch, []model.Protocol{model.Tunnel, model.Mixed, model.HTTP, model.Mieru, model.SSH, model.Snell}).Count(&localOwners).Error; err != nil {
				return err
			}
			if localOwners != 0 {
				return remoteClientPolicyScopeError()
			}
		}
		var currentIDs, remoteIDs []int
		if err := tx.Model(&model.ClientInbound{}).Where("client_id IN ? AND inbound_id = ?", batch, inboundID).Pluck("client_id", &currentIDs).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ClientInbound{}).
			Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
			Where("client_inbounds.client_id IN ? AND inbounds.node_id IS NOT NULL", batch).
			Distinct("client_inbounds.client_id").Pluck("client_inbounds.client_id", &remoteIDs).Error; err != nil {
			return err
		}
		for _, id := range currentIDs {
			linked[id] = true
		}
		for _, id := range remoteIDs {
			remote[id] = true
		}
	}
	for _, client := range clients {
		record := existing[strings.TrimSpace(client.Email)]
		if record == nil {
			if inbound.NodeID != nil && client.Policy != nil {
				return remoteClientPolicyScopeError()
			}
			continue
		}
		changed := client.Policy != nil && !sameClientPolicy(client.Policy, record.Policy)
		if localOwnedResource && remote[record.Id] {
			return remoteClientPolicyScopeError()
		}
		attaching := !linked[record.Id] && (client.Policy != nil || record.Policy != nil || record.DesiredPolicyVersion != 0)
		if (changed || attaching) && (inbound.NodeID != nil || remote[record.Id]) {
			return remoteClientPolicyScopeError()
		}
	}
	return nil
}

func remoteClientPolicyScopeError() error {
	return fmt.Errorf("%w: client traffic policy requires local-only bindings; remote coordinated budgets and application receipts are not supported", ErrClientPolicyLedger)
}

func sameClientPolicy(a, b *model.ClientPolicyOptions) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func guardClientPolicyTargets(tx *gorm.DB, record *model.ClientRecord, policy *model.ClientPolicyOptions, inboundIDs []int, attaching bool) error {
	localOwnedResource := false
	for _, batch := range chunkInts(inboundIDs, 400) {
		var count int64
		if err := tx.Model(&model.Inbound{}).Where("id IN ? AND node_id IS NULL AND protocol IN ?", batch, []model.Protocol{model.Tunnel, model.Mixed, model.HTTP, model.Mieru, model.SSH, model.Snell}).Count(&count).Error; err != nil {
			return err
		}
		localOwnedResource = localOwnedResource || count != 0
	}
	if record != nil && !localOwnedResource {
		var count int64
		if err := tx.Model(&model.ClientInbound{}).Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
			Where("client_inbounds.client_id = ? AND inbounds.node_id IS NULL AND inbounds.protocol IN ?", record.Id, []model.Protocol{model.Tunnel, model.Mixed, model.HTTP, model.Mieru, model.SSH, model.Snell}).Count(&count).Error; err != nil {
			return err
		}
		localOwnedResource = count != 0
	}
	if !localOwnedResource {
		if attaching {
			if policy == nil && (record == nil || record.Policy == nil && record.DesiredPolicyVersion == 0) {
				return nil
			}
		} else if policy == nil || record != nil && sameClientPolicy(policy, record.Policy) {
			return nil
		}
	}
	for _, batch := range chunkInts(inboundIDs, 400) {
		var count int64
		if err := tx.Model(&model.Inbound{}).Where("id IN ? AND node_id IS NOT NULL", batch).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return remoteClientPolicyScopeError()
		}
	}
	if record != nil {
		var count int64
		if err := tx.Model(&model.ClientInbound{}).
			Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
			Where("client_inbounds.client_id = ? AND inbounds.node_id IS NOT NULL", record.Id).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return remoteClientPolicyScopeError()
		}
	}
	return nil
}

// Validate the saved wire settings before identity filters can hide policy entries.
func guardMirroredClientPolicies(tx *gorm.DB, inboundID int, settings string) error {
	if trimmed := strings.TrimSpace(settings); trimmed == "" || trimmed == "null" {
		return nil
	}
	clients, err := ParseInboundSettingsClients(settings)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(clients, func(client model.Client) bool { return client.Policy != nil }) {
		if err := validateClientsSettings(clients); err != nil {
			return err
		}
	}
	emails := make([]string, 0, len(clients))
	for _, client := range clients {
		emails = append(emails, strings.TrimSpace(client.Email))
	}
	existing := make(map[string]*model.ClientRecord)
	for _, batch := range chunkStrings(emails, 400) {
		var rows []model.ClientRecord
		if err := tx.Where("email IN ?", batch).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			existing[rows[i].Email] = &rows[i]
		}
	}
	return guardRemoteClientPolicyAttachments(tx, inboundID, existing, clients)
}
