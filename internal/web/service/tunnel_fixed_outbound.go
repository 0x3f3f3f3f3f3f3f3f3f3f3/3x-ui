package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func tunnelFixedOutbound(inbound *model.Inbound) (string, error) {
	if inbound.Protocol != model.Tunnel || strings.TrimSpace(inbound.Settings) == "" {
		return "", nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &fields); err != nil {
		return "", fmt.Errorf("Tunnel outbound settings: %w", err)
	}
	for key := range fields {
		if key != "outboundTag" && strings.EqualFold(key, "outboundTag") {
			return "", fmt.Errorf("Tunnel outbound field must be spelled outboundTag")
		}
	}
	if len(fields["outboundTag"]) == 0 {
		return "", nil
	}
	var tag string
	if err := json.Unmarshal(fields["outboundTag"], &tag); err != nil {
		return "", fmt.Errorf("Tunnel outboundTag must be a string: %w", err)
	}
	if tag != "" && inbound.NodeID != nil {
		return "", fmt.Errorf("fixed outbound requires a local Tunnel listener")
	}
	return tag, nil
}

func validateTunnelFixedOutboundOwner(tx *gorm.DB, inbound *model.Inbound) error {
	tag, err := tunnelFixedOutbound(inbound)
	if err != nil || tag == "" || !inbound.Enable {
		return err
	}
	return validateTunnelCanonicalOwner(tx, inbound, "fixed outbound")
}

func validateTunnelFixedOutboundSelection(tx *gorm.DB, inbound *model.Inbound) error {
	tag, err := tunnelFixedOutbound(inbound)
	if err != nil || tag == "" || !inbound.Enable {
		return err
	}
	template, err := (&SettingService{}).getStringFromDB(tx, "xrayTemplateConfig")
	if err != nil {
		return err
	}
	var config xray.Config
	if err := json.Unmarshal([]byte(template), &config); err != nil {
		return fmt.Errorf("Tunnel outbound template: %w", err)
	}
	prepend, appendList, err := (&OutboundSubscriptionService{}).activeOutboundsSplitFromDB(tx)
	if err != nil {
		return err
	}
	mergeSubscriptionOutbounds(&config, prepend, appendList)
	var outbounds []struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(config.OutboundConfigs, &outbounds); err != nil {
		return fmt.Errorf("Tunnel outbounds: %w", err)
	}
	matches := 0
	for _, outbound := range outbounds {
		if outbound.Tag == tag {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("Tunnel outbound %q must name one available concrete outbound (found %d)", tag, matches)
	}
	return nil
}
