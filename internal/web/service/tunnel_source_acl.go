package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func tunnelSourceCIDRs(inbound *model.Inbound) ([]string, error) {
	if strings.TrimSpace(inbound.Settings) == "" {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &fields); err != nil {
		if inbound.Protocol == model.Tunnel {
			return nil, fmt.Errorf("Tunnel source ACL settings: %w", err)
		}
		return nil, nil
	}
	for key := range fields {
		if key != "allowedSourceCidrs" && strings.EqualFold(key, "allowedSourceCidrs") {
			return nil, fmt.Errorf("Tunnel source ACL field must be spelled allowedSourceCidrs")
		}
	}
	raw := fields["allowedSourceCidrs"]
	if len(raw) == 0 {
		return nil, nil
	}
	var sources []string
	if err := json.Unmarshal(raw, &sources); err != nil {
		return nil, fmt.Errorf("Tunnel source ACL must be an array of CIDRs: %w", err)
	}
	return sources, nil
}

func validateTunnelSourceACLConfig(inbound *model.Inbound) error {
	sources, err := tunnelSourceCIDRs(inbound)
	if err != nil || len(sources) == 0 {
		return err
	}
	if inbound.Protocol != model.Tunnel || inbound.NodeID != nil {
		return fmt.Errorf("source ACL requires a local Tunnel listener")
	}
	raw, err := json.Marshal(inbound.GenXrayInboundConfig())
	if err != nil {
		return fmt.Errorf("Tunnel source ACL configuration: %w", err)
	}
	var config conf.InboundDetourConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return fmt.Errorf("Tunnel source ACL configuration: %w", err)
	}
	if _, err := config.Build(); err != nil {
		return fmt.Errorf("Tunnel source ACL configuration: %w", err)
	}
	return nil
}

func validateTunnelSourceACLOwner(tx *gorm.DB, inbound *model.Inbound) error {
	sources, err := tunnelSourceCIDRs(inbound)
	if err != nil || len(sources) == 0 {
		return err
	}
	if inbound.Protocol != model.Tunnel || inbound.NodeID != nil {
		return fmt.Errorf("source ACL requires a local Tunnel listener")
	}
	if !inbound.Enable {
		return nil
	}
	return validateTunnelCanonicalOwner(tx, inbound, "source ACL")
}

func validateTunnelCanonicalOwner(tx *gorm.DB, inbound *model.Inbound, feature string) error {
	var owners []string
	if err := tx.Table("client_inbounds ci").Select("c.stable_id").
		Joins("JOIN clients c ON c.id = ci.client_id").Where("ci.inbound_id = ?", inbound.Id).
		Pluck("c.stable_id", &owners).Error; err != nil {
		return err
	}
	if len(owners) != 1 || owners[0] == "" {
		return fmt.Errorf("enabled Tunnel %s requires exactly one canonical owner", feature)
	}
	return nil
}

func validateStoredTunnelOwnerSettings(tx *gorm.DB, inboundID int) error {
	var inbound model.Inbound
	if err := tx.Select("id", "protocol", "settings", "enable", "node_id").Where("id = ?", inboundID).Find(&inbound).Error; err != nil {
		return err
	}
	if inbound.Id == 0 {
		return nil
	}
	if err := validateTunnelSourceACLOwner(tx, &inbound); err != nil {
		return err
	}
	return validateTunnelFixedOutboundOwner(tx, &inbound)
}
