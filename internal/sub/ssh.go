package sub

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

var errSSHClientFormat = errors.New("SSH requires native OpenSSH files: use format=ssh, format=ssh-known-hosts and format=ssh-instructions on the subscription URL")

func (s *SubService) GetSSHExport(subID, host, format string) (string, string, error) {
	request := s.ForRequest(host)
	inbounds, err := request.getInboundsBySubId(subID)
	if err != nil {
		return "", "", err
	}
	var body strings.Builder
	var emails []string
	for _, inbound := range inbounds {
		if inbound.Protocol != model.SSH || inbound.ExcludeFromSub {
			continue
		}
		clients := request.matchingClients(inbound, subID)
		endpoints, err := request.sshEndpoints(inbound)
		if err != nil {
			return "", "", err
		}
		for _, client := range clients {
			if !client.Enable || client.ExpiryTime > 0 && client.ExpiryTime <= time.Now().UnixMilli() {
				continue
			}
			for n, endpoint := range endpoints {
				if endpoint.ForceTls != "" && endpoint.ForceTls != "same" && endpoint.ForceTls != "none" {
					return "", "", fmt.Errorf("native SSH export does not support TLS endpoint wrappers")
				}
				profile, err := service.SSHClientExport(inbound, client.Email, endpoint.Address, endpoint.Port, n+1)
				if err != nil {
					return "", "", err
				}
				switch format {
				case "ssh":
					body.WriteString(profile.Config)
				case "ssh-known-hosts":
					body.WriteString(profile.KnownHosts)
				case "ssh-instructions":
					body.WriteString(profile.Instructions)
					body.WriteString("\n")
				default:
					return "", "", errSSHClientFormat
				}
			}
			emails = append(emails, client.Email)
		}
	}
	traffic, _ := request.AggregateTrafficByEmails(emails)
	return body.String(), request.subscriptionUserinfo(traffic), nil
}

// Native SSH can advertise address/port overrides, but cannot silently lose
// transport, TLS or mux options configured on a managed share host.
func (s *SubService) sshEndpoints(inbound *model.Inbound) ([]ShareEndpoint, error) {
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
			return nil, fmt.Errorf("native SSH export does not support managed host security wrappers")
		}
		raw := hostToExternalProxyMap(host, s.resolveInboundAddress(inbound), inbound.Port)
		for field := range raw {
			switch field {
			case "dest", "port", "forceTls", "remark", "serverDescription", "isHost":
			default:
				return nil, fmt.Errorf("native SSH export does not support managed host option %s", field)
			}
		}
		endpoints = append(endpoints, externalProxyToEndpoint(raw))
	}
	if len(endpoints) == 0 {
		endpoints = []ShareEndpoint{s.inboundDefaultEndpoint(inbound)}
	}
	return endpoints, nil
}
