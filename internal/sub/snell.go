package sub

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

var errSnellClientFormat = errors.New("Snell requires native client configuration: use format=snell-surge or format=snell-json on the subscription URL")

func (s *SubService) GetSnellExport(subID, host, format string) (string, string, error) {
	if format != "snell-surge" && format != "snell-json" {
		return "", "", errSnellClientFormat
	}
	request := s.ForRequest(host)
	inbounds, err := request.getInboundsBySubId(subID)
	if err != nil {
		return "", "", err
	}
	var policies, names, emails []string
	var outbounds []map[string]any
	for _, inbound := range inbounds {
		if inbound.Protocol != model.Snell || inbound.ExcludeFromSub {
			continue
		}
		clients := request.matchingClients(inbound, subID)
		endpoints, err := request.snellEndpoints(inbound)
		if err != nil {
			return "", "", err
		}
		for _, client := range clients {
			if !client.Enable || client.ExpiryTime > 0 && client.ExpiryTime <= time.Now().UnixMilli() {
				continue
			}
			for n, endpoint := range endpoints {
				if endpoint.ForceTls != "" && endpoint.ForceTls != "same" && endpoint.ForceTls != "none" {
					return "", "", errors.New("native Snell export does not support TLS endpoint wrappers")
				}
				config, err := service.SnellClientExport(inbound, client.Email, endpoint.Address, endpoint.Port)
				if err != nil {
					return "", "", err
				}
				raw, err := json.Marshal(config)
				if err != nil {
					return "", "", err
				}
				var settings map[string]any
				if err := json.Unmarshal(raw, &settings); err != nil {
					return "", "", err
				}
				// Xray's Address.String formats IPv6 for sockets; JSON uses a bare host.
				settings["address"] = strings.Trim(config.Address.String(), "[]")
				tag := fmt.Sprintf("snell-%d-%d", inbound.Id, n+1)
				outbounds = append(outbounds, map[string]any{"tag": tag, "protocol": "snell", "settings": settings})
				if format == "snell-surge" {
					name := request.endpointRemark(inbound, client.Email, endpoint.ep, "") + " / " + tag
					policy, err := service.SnellSurgePolicy(name, config)
					if err != nil {
						return "", "", err
					}
					quoted, err := service.SnellSurgeQuote(name)
					if err != nil {
						return "", "", err
					}
					policies, names = append(policies, policy), append(names, quoted)
				}
			}
			emails = append(emails, client.Email)
		}
	}
	if len(outbounds) == 0 {
		return "", "", nil
	}
	traffic, _ := request.AggregateTrafficByEmails(emails)
	header := request.subscriptionUserinfo(traffic)
	if format == "snell-surge" {
		body := "# Quoted values require Surge iOS 5.21.0+ / Mac 6.8.0+.\n# Snell v5 QUIC Proxy Mode is automatic; there is no QUIC profile parameter.\n[Proxy]\n" + strings.Join(policies, "\n") +
			"\n\n[Proxy Group]\nSnell = select, " + strings.Join(names, ", ") + "\n\n[Rule]\nFINAL,Snell\n"
		return body, header, nil
	}
	config := map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": []map[string]any{{
		"tag": "snell-local", "listen": "127.0.0.1", "port": 1080,
		"protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true, "udpFullDatagrams": true},
	}}, "outbounds": outbounds}
	raw, err := json.MarshalIndent(config, "", "  ")
	return string(raw), header, err
}

func (s *SubService) snellEndpoints(inbound *model.Inbound) ([]ShareEndpoint, error) {
	var hosts []*model.Host
	if err := database.GetDB().Where("inbound_id = ? AND is_disabled = ?", inbound.Id, false).Order("sort_order ASC, id ASC").Find(&hosts).Error; err != nil {
		return nil, err
	}
	var endpoints []ShareEndpoint
	for _, host := range hosts {
		if slices.Contains(host.ExcludeFromSubTypes, "raw") {
			continue
		}
		if host.Security != "" && host.Security != "same" && host.Security != "none" {
			return nil, errors.New("native Snell export does not support managed host security wrappers")
		}
		raw := hostToExternalProxyMap(host, s.resolveInboundAddress(inbound), inbound.Port)
		for field := range raw {
			switch field {
			case "dest", "port", "forceTls", "remark", "serverDescription", "isHost":
			default:
				return nil, fmt.Errorf("native Snell export does not support managed host option %s", field)
			}
		}
		endpoints = append(endpoints, externalProxyToEndpoint(raw))
	}
	if len(hosts) == 0 {
		endpoints = []ShareEndpoint{s.inboundDefaultEndpoint(inbound)}
	}
	return endpoints, nil
}
