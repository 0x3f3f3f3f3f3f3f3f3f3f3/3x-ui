package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var ErrSnellOwnerConflict = errors.New("Snell listener can belong to only one client")

// Remove the selected canonical owner even when the presentation mirror is stale.
func (s *ClientService) removeSnellOwners(inboundSvc *InboundService, inbound *model.Inbound, records []*model.ClientRecord, keepTraffic bool) (bool, error) {
	wanted := make(map[int]string, len(records))
	for _, record := range records {
		if record != nil {
			wanted[record.Id] = record.StableID
		}
	}
	var saved model.Inbound
	changed := false
	err := runSerializedTx(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&saved, inbound.Id).Error; err != nil {
			return err
		}
		if saved.Protocol != model.Snell || saved.NodeID != nil {
			return ErrSnellOwnerConflict
		}
		var owners []model.ClientRecord
		if err := tx.Table("clients c").Select("c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").
			Clauses(clause.Locking{Strength: "UPDATE"}).Where("ci.inbound_id = ?", saved.Id).Find(&owners).Error; err != nil {
			return err
		}
		if len(owners) > 1 {
			return ErrSnellOwnerConflict
		}
		if len(owners) == 0 {
			return nil
		}
		owner := owners[0]
		stableID, selected := wanted[owner.Id]
		if !selected {
			return nil
		}
		if stableID == "" || stableID != owner.StableID {
			return ErrSnellOwnerConflict
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(saved.Settings), &settings); err != nil {
			return err
		}
		for _, key := range []string{"psk", "clientId", "client_id", "email", "level", "users"} {
			delete(settings, key)
		}
		settings["clients"] = json.RawMessage(`[]`)
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		saved.Settings = string(raw)
		if err := tx.Model(&model.Inbound{}).Where("id = ?", saved.Id).Update("settings", saved.Settings).Error; err != nil {
			return err
		}
		if err := s.ApplyInboundClientDelta(tx, saved.Id, nil, []string{owner.Email}); err != nil {
			return err
		}
		if !keepTraffic {
			var siblings int64
			if err := tx.Model(&model.ClientInbound{}).Where("client_id = ?", owner.Id).Count(&siblings).Error; err != nil {
				return err
			}
			if siblings == 0 {
				if err := inboundSvc.DelClientIPs(tx, owner.Email); err != nil {
					return err
				}
				if err := inboundSvc.DelClientStat(tx, owner.Email); err != nil {
					return err
				}
			}
		}
		saved.Enable, changed = false, true
		return nil
	})
	if err != nil || !changed {
		return false, err
	}
	if handled, err := inboundSvc.reconcileManagedChange(&saved); handled {
		return err != nil, err
	}
	return stopOwnedTunnelListener(inboundSvc, &saved)
}

func validateProvidedSnellCredentials(client model.Client) error {
	if len(client.SnellPSK) > 255 || !utf8.ValidString(client.SnellPSK) {
		return errors.New("Snell PSK must be valid UTF-8 and at most 255 bytes")
	}
	return nil
}

// Resolve membership omissions from the current canonical row inside SQL's writer.
func resolveSnellClientCredentials(client *model.Client, stored *model.ClientRecord, generate bool) error {
	if err := validateProvidedSnellCredentials(*client); err != nil {
		return err
	}
	if client.SnellPSK == "" && stored != nil {
		client.SnellPSK = stored.SnellPSK
	}
	if generate && client.SnellPSK == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		client.SnellPSK = base64.RawURLEncoding.EncodeToString(secret)
	}
	return validateProvidedSnellCredentials(*client)
}

func snellNativeOptions(inbound *model.Inbound, psk string) (*conf.SnellServerConfig, error) {
	var native conf.SnellServerConfig
	if err := json.Unmarshal([]byte(inbound.Settings), &native); err != nil {
		return nil, err
	}
	// This fixed identity is used only for pure option validation. SQL alone binds
	// the runtime owner in bindManagedSnellIdentity; this value is never persisted.
	native.ClientID, native.Email, native.PSK = "00000000-0000-4000-8000-000000000001", "snell-options-validation", psk
	return &native, nil
}

func validateSnellListenerPSK(inbound *model.Inbound, psk string) error {
	if err := validateProvidedSnellCredentials(model.Client{SnellPSK: psk}); err != nil {
		return err
	}
	native, err := snellNativeOptions(inbound, psk)
	if err != nil {
		return err
	}
	_, err = native.Build()
	return err
}

func rejectSnellRuntimeOwnerIDs(raw string) error {
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return err
	}
	for _, key := range []string{"clientId", "client_id", "psk", "email", "users", "level"} {
		if _, exists := settings[key]; exists {
			return fmt.Errorf("Snell runtime %s is derived from SQL ownership", key)
		}
	}
	var clients []map[string]json.RawMessage
	if raw, exists := settings["clients"]; exists {
		if err := json.Unmarshal(raw, &clients); err != nil {
			return err
		}
	}
	for _, client := range clients {
		for _, key := range []string{"clientId", "client_id"} {
			if _, exists := client[key]; exists {
				return errors.New("Snell runtime client ID is derived from SQL ownership")
			}
		}
	}
	return nil
}

func prepareSnellInbound(inbound *model.Inbound) error {
	if inbound.Protocol != model.Snell {
		return nil
	}
	if inbound.NodeID != nil {
		return remoteClientPolicyScopeError()
	}
	if err := rejectSnellRuntimeOwnerIDs(inbound.Settings); err != nil {
		return err
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return err
	}
	if settings == nil {
		return errors.New("Snell settings must be an object")
	}
	allowed := map[string]bool{"clients": true, "version": true, "obfs": true, "mode": true, "quic": true}
	for key := range settings {
		if !allowed[key] {
			return fmt.Errorf("unsupported panel Snell setting %q", key)
		}
	}
	if inbound.Port < 1 || inbound.Port > 65535 {
		return errors.New("Snell requires one nonzero listener port")
	}
	if inbound.Listen != "" && net.ParseIP(inbound.Listen) == nil {
		return errors.New("Snell requires an IP listener")
	}
	var stream map[string]json.RawMessage
	if strings.TrimSpace(inbound.StreamSettings) != "" {
		if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
			return err
		}
		if stream == nil {
			return errors.New("Snell stream settings must be an object")
		}
	}
	for key := range stream {
		if key != "network" && key != "security" && key != "sockopt" {
			return errors.New("Snell does not support stream wrappers")
		}
	}
	var network, security string
	if raw, ok := stream["network"]; ok {
		if err := json.Unmarshal(raw, &network); err != nil {
			return err
		}
	}
	if raw, ok := stream["security"]; ok {
		if err := json.Unmarshal(raw, &security); err != nil {
			return err
		}
	}
	if network != "" && network != "tcp" && network != "raw" || security != "" && security != "none" {
		return errors.New("Snell requires native TCP without TLS or transport wrappers")
	}
	if err := validateSnellListenerPSK(inbound, "snell-pure-option-validation"); err != nil {
		return err
	}
	// Build the original stream against a validation-only native owner before
	// normalization or SQL can discard malformed physical socket options.
	native, err := snellNativeOptions(inbound, "snell-pure-option-validation")
	if err != nil {
		return err
	}
	nativeRaw, err := json.Marshal(native)
	if err != nil {
		return err
	}
	candidate := *inbound
	candidate.Settings = string(nativeRaw)
	raw, err := json.Marshal(candidate.GenXrayInboundConfig())
	if err != nil {
		return err
	}
	var detour conf.InboundDetourConfig
	if err := json.Unmarshal(raw, &detour); err != nil {
		return err
	}
	if _, err := detour.Build(); err != nil {
		return err
	}
	clients, err := ParseInboundSettingsClients(inbound.Settings)
	if err != nil {
		return err
	}
	if len(clients) > 1 {
		return ErrSnellOwnerConflict
	}
	if inbound.Enable && len(clients) == 0 && inbound.OwnerClientID == nil {
		return errors.New("enabled Snell listener requires one owner")
	}
	for _, client := range clients {
		if strings.TrimSpace(client.Email) == "" {
			return errors.New("Snell owner label is required")
		}
		if err := validateProvidedSnellCredentials(client); err != nil {
			return err
		}
		if client.SnellPSK != "" {
			if err := validateSnellListenerPSK(inbound, client.SnellPSK); err != nil {
				return err
			}
		}
	}
	return nil
}

func prepareSnellOwnerCommand(inbound *model.Inbound) error {
	if inbound.OwnerClientID == nil {
		return nil
	}
	if inbound.NodeID != nil {
		return remoteClientPolicyScopeError()
	}
	if id, err := uuid.Parse(*inbound.OwnerClientID); err != nil || id.String() != *inbound.OwnerClientID {
		return errors.New("ownerClientId requires an existing canonical client UUID")
	}
	if len(inbound.ClientStats) != 0 {
		return errors.New("ownerClientId cannot be combined with clientStats")
	}
	clients, err := ParseInboundSettingsClients(inbound.Settings)
	if err != nil {
		return err
	}
	if len(clients) != 0 {
		return errors.New("Snell ownerClientId cannot be combined with clients")
	}
	return setTunnelOwnerClients(inbound, nil)
}

func resolveSnellOwnerCommand(tx *gorm.DB, inbound *model.Inbound) (*model.ClientRecord, error) {
	if inbound.OwnerClientID == nil {
		return nil, nil
	}
	var owner model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id = ?", *inbound.OwnerClientID).First(&owner).Error; err != nil {
		return nil, err
	}
	if err := validateLocalClientPolicyResetScope(tx, []string{owner.StableID}); err != nil {
		return nil, err
	}
	if err := setTunnelOwnerClients(inbound, &owner); err != nil {
		return nil, err
	}
	return &owner, nil
}

func resolveSnellInboundCredentials(tx *gorm.DB, inbound *model.Inbound) error {
	if inbound.Protocol != model.Snell {
		return nil
	}
	if inbound.NodeID != nil {
		return remoteClientPolicyScopeError()
	}
	if _, err := resolveSnellOwnerCommand(tx, inbound); err != nil {
		return err
	}
	clients, err := ParseInboundSettingsClients(inbound.Settings)
	if err != nil {
		return err
	}
	if len(clients) > 1 {
		return ErrSnellOwnerConflict
	}
	for i := range clients {
		var stored model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email = ?", strings.TrimSpace(clients[i].Email)).Find(&stored).Error; err != nil {
			return err
		}
		if stored.Id != 0 {
			if err := validateLocalClientPolicyResetScope(tx, []string{stored.StableID}); err != nil {
				return err
			}
		}
		if stored.Id == 0 && clients[i].SubID == "" {
			clients[i].SubID = strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		if err := resolveSnellClientCredentials(&clients[i], &stored, true); err != nil {
			return err
		}
		if err := validateSnellListenerPSK(inbound, clients[i].SnellPSK); err != nil {
			return err
		}
	}
	if clients == nil {
		clients = []model.Client{}
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return err
	}
	settings["clients"], err = json.Marshal(clients)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(settings)
	if err == nil {
		inbound.Settings = string(raw)
	}
	return err
}

// Lock the physical resource before checking prospective membership. Disabled
// owners still reserve the listener; only actual detach/delete releases it.
func validateSnellOwnerLinks(tx *gorm.DB, inboundID int, clients []model.Client, detach []string, prune bool) (bool, error) {
	var inbound model.Inbound
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND protocol = ?", inboundID, model.Snell).Find(&inbound).Error; err != nil {
		return false, err
	}
	if inbound.Id == 0 {
		return false, nil
	}
	if inbound.NodeID != nil {
		return false, remoteClientPolicyScopeError()
	}
	var emails []string
	if err := tx.Table("clients c").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").Where("ci.inbound_id = ?", inboundID).Pluck("c.email", &emails).Error; err != nil {
		return false, err
	}
	owners := make(map[string]bool)
	if !prune {
		for _, email := range emails {
			owners[strings.TrimSpace(email)] = true
		}
	}
	for _, email := range detach {
		delete(owners, strings.TrimSpace(email))
	}
	for _, client := range clients {
		if email := strings.TrimSpace(client.Email); email != "" {
			owners[email] = true
		}
	}
	if len(owners) > 1 {
		return false, ErrSnellOwnerConflict
	}
	return len(emails) != 0 && len(owners) == 0, nil
}

func validateLinkedSnellCredentialChanges(tx *gorm.DB, targetID int, clients []model.Client, existing map[string]*model.ClientRecord) error {
	for _, client := range clients {
		row := existing[strings.TrimSpace(client.Email)]
		if row == nil {
			continue
		}
		if err := resolveSnellClientCredentials(&client, row, false); err != nil {
			return err
		}
		if client.SnellPSK == row.SnellPSK {
			continue
		}
		var listeners []model.Inbound
		if err := tx.Table("inbounds i").Select("i.*").Joins("JOIN client_inbounds ci ON ci.inbound_id = i.id").Where("ci.client_id = ? AND i.protocol = ? AND i.id <> ?", row.Id, model.Snell, targetID).Find(&listeners).Error; err != nil {
			return err
		}
		for _, listener := range listeners {
			if err := validateSnellListenerPSK(&listener, client.SnellPSK); err != nil {
				return err
			}
		}
	}
	return nil
}

func refreshSnellCredentialMirrors(tx *gorm.DB, targetID int, clientIDs []int) error {
	ids := []int{}
	if targetID > 0 {
		ids = append(ids, targetID)
	}
	if len(clientIDs) > 0 {
		var linked []int
		if err := tx.Table("client_inbounds ci").Joins("JOIN inbounds i ON i.id = ci.inbound_id").Where("ci.client_id IN ? AND i.protocol = ?", clientIDs, model.Snell).Distinct().Pluck("ci.inbound_id", &linked).Error; err != nil {
			return err
		}
		ids = append(ids, linked...)
	}
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		var inbound model.Inbound
		if err := tx.First(&inbound, id).Error; err != nil {
			return err
		}
		if inbound.Protocol != model.Snell {
			continue
		}
		var owners []model.ClientRecord
		if err := tx.Table("clients c").Select("c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").Where("ci.inbound_id = ?", id).Find(&owners).Error; err != nil {
			return err
		}
		if len(owners) > 1 {
			return ErrSnellOwnerConflict
		}
		var owner *model.ClientRecord
		if len(owners) == 1 {
			owner = &owners[0]
		}
		if err := setTunnelOwnerClients(&inbound, owner); err != nil {
			return err
		}
		if err := tx.Model(&model.Inbound{}).Where("id = ?", id).UpdateColumn("settings", inbound.Settings).Error; err != nil {
			return err
		}
	}
	return nil
}

func bindManagedSnellIdentity(inbound *xray.InboundConfig, records []model.ClientRecord) error {
	if len(records) != 1 || records[0].StableID == "" {
		return fmt.Errorf("%w: Snell requires one canonical owner", ErrManagedConfigStale)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
		return err
	}
	var email, psk string
	if err := json.Unmarshal(settings["email"], &email); err != nil {
		return err
	}
	if err := json.Unmarshal(settings["psk"], &psk); err != nil {
		return err
	}
	record := records[0]
	if record.Email != email || record.SnellPSK != psk {
		return fmt.Errorf("%w: Snell authentication changed", ErrManagedConfigStale)
	}
	delete(settings, "clients")
	delete(settings, "client_id")
	settings["clientId"], _ = json.Marshal(record.StableID)
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	native := conf.SnellServerConfig{}
	if err := json.Unmarshal(raw, &native); err != nil {
		return err
	}
	if _, err := native.Build(); err != nil {
		return err
	}
	inbound.Settings = raw
	return nil
}
