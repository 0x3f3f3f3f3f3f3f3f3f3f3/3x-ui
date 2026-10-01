package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xtls/xray-core/infra/conf"
	"golang.org/x/net/idna"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// SnellClientExport resolves the current exclusive SQL owner and native endpoint.
func SnellClientExport(inbound *model.Inbound, email, address string, port int) (*conf.SnellClientConfig, error) {
	if inbound == nil || inbound.Protocol != model.Snell || port < 1 || port > 65535 {
		return nil, errors.New("Snell export requires a native listener and valid endpoint")
	}
	db := database.GetDB()
	var saved model.Inbound
	if err := db.First(&saved, inbound.Id).Error; err != nil {
		return nil, err
	}
	var owners []model.ClientRecord
	if err := db.Table("clients c").Select("c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").
		Where("ci.inbound_id = ?", saved.Id).Find(&owners).Error; err != nil {
		return nil, err
	}
	if len(owners) != 1 || owners[0].StableID == "" || owners[0].Email != email {
		return nil, ErrSnellOwnerConflict
	}
	owner := owners[0]
	if saved.Protocol != model.Snell || saved.NodeID != nil || !saved.Enable || saved.ExcludeFromSub || !owner.Enable || owner.ExpiryTime > 0 && owner.ExpiryTime <= time.Now().UnixMilli() {
		return nil, errors.New("inactive native Snell owner or resource")
	}
	// Presentation JSON cannot supply the exported credential or account identity.
	if err := setTunnelOwnerClients(&saved, &owner); err != nil {
		return nil, err
	}
	if err := prepareSnellInbound(&saved); err != nil {
		return nil, err
	}
	native, err := snellNativeOptions(&saved, owner.SnellPSK)
	if err != nil {
		return nil, err
	}
	address = strings.TrimPrefix(strings.TrimSuffix(address, "]"), "[")
	if address == "" || strings.TrimSpace(address) != address || strings.ContainsAny(address, "/,?#@\"'\\") {
		return nil, errors.New("Snell export requires an explicit server hostname or IP address")
	}
	for _, char := range address {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return nil, errors.New("Snell export requires an explicit server hostname or IP address")
		}
	}
	if net.ParseIP(address) == nil {
		if strings.Contains(address, ":") {
			return nil, errors.New("Snell export requires a valid IPv6 address")
		}
		address, err = idna.Lookup.ToASCII(address)
		if err != nil {
			return nil, err
		}
	}
	fields := map[string]any{
		"version": native.Version, "address": address, "port": port, "psk": owner.SnellPSK,
		"obfs": native.Obfs, "mode": native.Mode, "reuse": true, "quic": native.QUIC,
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	var client conf.SnellClientConfig
	if err := json.Unmarshal(raw, &client); err != nil {
		return nil, err
	}
	if _, err := client.Build(); err != nil {
		return nil, err
	}
	return &client, nil
}

// SnellSurgeQuote preserves literal UTF-8 using Surge's documented value escapes.
func SnellSurgeQuote(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("Snell Surge values require valid UTF-8; use format=snell-json")
	}
	for _, char := range value {
		if unicode.IsControl(char) || char == '\u2028' || char == '\u2029' {
			return "", errors.New("Snell Surge text cannot represent multiline or control values; use format=snell-json")
		}
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`, nil
}

func SnellSurgePolicy(name string, client *conf.SnellClientConfig) (string, error) {
	if client == nil || client.Address == nil {
		return "", errors.New("Snell Surge policy requires a native endpoint")
	}
	if _, err := client.Build(); err != nil {
		return "", err
	}
	quoted := make([]string, 0, 3)
	for _, value := range []string{name, strings.Trim(client.Address.String(), "[]"), client.PSK} {
		q, err := SnellSurgeQuote(value)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, q)
	}
	line := fmt.Sprintf("%s = snell, %s, %d, psk=%s, version=%d, reuse=%t", quoted[0], quoted[1], client.Port, quoted[2], client.Version, client.Reuse)
	if client.Obfs == "http" {
		line += ", obfs=http"
	}
	if client.Version == 6 && client.Mode != "" {
		line += ", mode=" + client.Mode
	}
	return line, nil
}
