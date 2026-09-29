package service

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"

	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func (c *compiledManagedConfig) checkLegacyAccounting(current *xray.Config) error {
	unavailable := fmt.Errorf("%w: legacy configuration has no verified per-client accounting", xray.ErrTrafficDrainCapability)
	if len(current.Stats) == 0 || string(current.Stats) == "null" {
		return unavailable
	}
	var policy conf.PolicyConfig
	if err := json.Unmarshal(current.Policy, &policy); err != nil {
		return unavailable
	}
	metered := func(level uint32) bool {
		p := policy.Levels[level]
		return p != nil && p.StatsUserUplink && p.StatsUserDownlink
	}
	var api conf.APIConfig
	if err := json.Unmarshal(current.API, &api); err != nil {
		return unavailable
	}
	expectedProtocols := make(map[string]string, len(c.config.InboundConfigs))
	for _, inbound := range c.config.InboundConfigs {
		expectedProtocols[inbound.Tag] = inbound.Protocol
	}
	for _, inbound := range current.InboundConfigs {
		owners, known := c.bindings[inbound.Tag]
		if !known && isLegacyControlInbound(current, inbound, api.Tag) {
			continue
		}
		if inbound.Protocol == "dokodemo-door" {
			inbound.Protocol = string(model.Tunnel)
		}
		if !known || expectedProtocols[inbound.Tag] != inbound.Protocol {
			return unavailable
		}
		switch model.Protocol(inbound.Protocol) {
		case model.Tunnel, model.VLESS, model.VMESS, model.Trojan, model.Shadowsocks:
		default:
			return unavailable
		}
		var settings struct {
			Email     string `json:"email"`
			UserLevel uint32 `json:"userLevel"`
			Clients   []struct {
				Email string `json:"email"`
				Level uint32 `json:"level"`
			} `json:"clients"`
			Users   json.RawMessage `json:"users"`
			Default json.RawMessage `json:"default"`
		}
		if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
			return unavailable
		}
		if inbound.Protocol == string(model.Tunnel) {
			if len(owners) != 1 || settings.Email == "" || settings.Email != owners[0].Email || !metered(settings.UserLevel) {
				return unavailable
			}
		} else {
			if inbound.Protocol == string(model.Shadowsocks) && len(settings.Clients) == 0 {
				return unavailable
			}
			if len(settings.Users) != 0 || len(settings.Default) != 0 {
				return unavailable
			}
			for _, client := range settings.Clients {
				if client.Email == "" || !metered(client.Level) {
					return unavailable
				}
			}
		}
		if err := bindManagedInboundIdentity(&inbound, owners); err != nil {
			return err
		}
	}
	return nil
}

func (c *compiledManagedConfig) checkLegacyTrafficOwners(tx *gorm.DB, batch *xray.TrafficBatch) error {
	byEmail := make(map[string]model.ClientRecord, len(c.records))
	for _, record := range c.records {
		byEmail[record.Email] = record
	}
	expected := make(map[string]model.ClientRecord)
	for _, traffic := range batch.ClientTraffics {
		if traffic == nil || (traffic.Up == 0 && traffic.Down == 0) {
			continue
		}
		record, ok := byEmail[traffic.Email]
		if !ok {
			return ErrManagedConfigStale
		}
		expected[record.StableID] = record
	}
	ids := make([]string, 0, len(expected))
	for id := range expected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, part := range chunkStrings(ids, 1000) {
		var records []model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", part).Order("stable_id").Find(&records).Error; err != nil {
			return err
		}
		if len(records) != len(part) {
			return ErrManagedConfigStale
		}
		emails := make([]string, 0, len(records))
		for _, record := range records {
			prior := expected[record.StableID]
			if record.Id != prior.Id || record.Email != prior.Email || record.UUID != prior.UUID || record.Password != prior.Password {
				return ErrManagedConfigStale
			}
			emails = append(emails, record.Email)
		}
		var rows []xray.ClientTraffic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email IN ?", emails).Order("email").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != len(emails) {
			return ErrManagedConfigStale
		}
	}
	return nil
}

func isLegacyControlInbound(config *xray.Config, inbound xray.InboundConfig, apiTag string) bool {
	if apiTag == "" || inbound.Tag != apiTag || (inbound.Protocol != string(model.Tunnel) && inbound.Protocol != "dokodemo-door") {
		return false
	}
	var listen string
	if json.Unmarshal(inbound.Listen, &listen) != nil || !net.ParseIP(listen).IsLoopback() {
		return false
	}
	var routing struct {
		Rules []map[string]json.RawMessage `json:"rules"`
	}
	if json.Unmarshal(config.RouterConfig, &routing) != nil || len(routing.Rules) == 0 {
		return false
	}
	rule := routing.Rules[0]
	if len(rule) != 3 {
		return false
	}
	var kind, outbound string
	var tags []string
	if json.Unmarshal(rule["type"], &kind) != nil || json.Unmarshal(rule["outboundTag"], &outbound) != nil || json.Unmarshal(rule["inboundTag"], &tags) != nil {
		return false
	}
	return kind == "field" && outbound == apiTag && len(tags) == 1 && tags[0] == apiTag
}
