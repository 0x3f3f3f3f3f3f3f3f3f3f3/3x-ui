package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/infra/conf"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func sshPublicKeyLines(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, "\n")
}

func bindManagedSSHIdentity(inbound *xray.InboundConfig, records []model.ClientRecord) error {
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
		return err
	}
	var users []*conf.SSHUser
	if err := json.Unmarshal(settings["users"], &users); err != nil {
		return err
	}
	if len(users) != len(records) {
		return fmt.Errorf("%w: SSH account membership changed", ErrManagedConfigStale)
	}
	byEmail := make(map[string]model.ClientRecord, len(records))
	for _, record := range records {
		if _, exists := byEmail[record.Email]; exists || record.StableID == "" {
			return fmt.Errorf("%w: SSH owner is not unique", ErrManagedConfigStale)
		}
		byEmail[record.Email] = record
	}
	seen := make(map[string]bool, len(users))
	for _, user := range users {
		if user == nil {
			return ErrManagedConfigStale
		}
		record, exists := byEmail[user.Email]
		if !exists || seen[user.Email] || user.Username != record.SSHUsername || user.Password != record.SSHPassword || !slices.Equal(user.PublicKeys, sshPublicKeyLines(record.SSHAuthorizedKeys)) {
			return fmt.Errorf("%w: SSH authentication changed", ErrManagedConfigStale)
		}
		user.ClientID = record.StableID
		seen[user.Email] = true
	}
	clients := make([]model.Client, 0, len(records))
	for _, row := range records {
		clients = append(clients, *row.ToClient())
	}
	if err := validateSSHListenerAccounts(&model.Inbound{Settings: string(inbound.Settings)}, clients); err != nil {
		return err
	}
	delete(settings, "clients")
	delete(settings, "clientId")
	delete(settings, "client_id")
	var err error
	settings["users"], err = json.Marshal(users)
	if err != nil {
		return err
	}
	inbound.Settings, err = json.Marshal(settings)
	return err
}

func normalizeSSHCredentials(client *model.Client) error {
	if len(client.SSHUsername) > 256 || !utf8.ValidString(client.SSHUsername) || strings.IndexFunc(client.SSHUsername, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("SSH username must be valid UTF-8, at most 256 bytes and contain no whitespace or control characters")
	}
	if len(client.SSHPassword) > 1024 || !utf8.ValidString(client.SSHPassword) {
		return fmt.Errorf("SSH password must be valid UTF-8 and at most 1024 bytes")
	}
	if client.ClearSSHPassword && client.SSHPassword != "" || client.ClearSSHAuthorizedKeys && client.SSHAuthorizedKeys != "" {
		return fmt.Errorf("SSH authentication cannot be supplied and explicitly cleared together")
	}
	keys := make([]string, 0)
	for _, line := range strings.Split(client.SSHAuthorizedKeys, "\n") {
		if len(line) > 16384 {
			return fmt.Errorf("SSH authorized-key lines must be at most 16384 bytes")
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(keys) == 16 {
			return fmt.Errorf("SSH accounts support at most 16 authorized keys")
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(options) != 0 || strings.TrimSpace(string(rest)) != "" {
			return fmt.Errorf("SSH authorized key is invalid or contains unsupported options")
		}
		if _, certificate := key.(*ssh.Certificate); certificate {
			return fmt.Errorf("SSH certificates are unsupported")
		}
		keys = append(keys, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	}
	slices.Sort(keys)
	client.SSHAuthorizedKeys = strings.Join(slices.Compact(keys), "\n")
	return nil
}

// The caller must own the serialized SQL writer when preserving omissions.
func resolveSSHClientCredentials(client *model.Client, stored *model.ClientRecord, generate bool) error {
	if err := normalizeSSHCredentials(client); err != nil {
		return err
	}
	if stored != nil {
		if client.SSHUsername == "" {
			client.SSHUsername = stored.SSHUsername
		}
		if client.SSHAuthorizedKeys == "" && !client.ClearSSHAuthorizedKeys {
			client.SSHAuthorizedKeys = stored.SSHAuthorizedKeys
		}
		if client.SSHPassword == "" && !client.ClearSSHPassword {
			client.SSHPassword = stored.SSHPassword
		}
	}
	if generate && client.SSHUsername == "" {
		client.SSHUsername = "ssh-" + uuid.NewString()
	}
	return normalizeSSHCredentials(client)
}

func sshPasswordAllowed(inbound *model.Inbound) (bool, error) {
	var settings struct {
		AllowPassword bool `json:"allowPassword"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return false, err
	}
	return settings.AllowPassword, nil
}

func validateSSHListenerAccounts(inbound *model.Inbound, clients []model.Client) error {
	allowPassword, err := sshPasswordAllowed(inbound)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(clients))
	for _, client := range clients {
		if err := normalizeSSHCredentials(&client); err != nil {
			return err
		}
		if client.SSHUsername == "" || client.SSHAuthorizedKeys == "" && (!allowPassword || client.SSHPassword == "") {
			return fmt.Errorf("SSH listener requires a username and a usable authorized key or explicitly enabled password authentication")
		}
		if seen[client.SSHUsername] {
			return fmt.Errorf("SSH username belongs to multiple listener clients")
		}
		seen[client.SSHUsername] = true
	}
	return nil
}

func prepareSSHInbound(inbound *model.Inbound) error {
	if inbound.Protocol != model.SSH {
		return nil
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return err
	}
	if settings == nil {
		return fmt.Errorf("SSH settings must be an object")
	}
	allowed := map[string]bool{"clients": true, "allowPassword": true, "handshakeTimeoutSeconds": true, "idleTimeoutSeconds": true, "channelOpenTimeoutSeconds": true, "maxAuthTries": true, "maxConnections": true, "maxConnectionsPerUser": true, "maxChannelsPerConnection": true, "maxChannels": true, "reverse": true}
	for key := range settings {
		if !allowed[key] {
			return fmt.Errorf("unsupported panel SSH setting %q", key)
		}
	}
	if inbound.Port < 1 || inbound.Port > 65535 {
		return fmt.Errorf("SSH requires one nonzero TCP listener port")
	}
	var stream map[string]json.RawMessage
	if strings.TrimSpace(inbound.StreamSettings) != "" {
		if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
			return err
		}
	}
	for key := range stream {
		if key != "network" && key != "security" && key != "sockopt" {
			return fmt.Errorf("SSH does not support stream wrappers")
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
		return fmt.Errorf("SSH supports native TCP without TLS or another transport wrapper")
	}
	var native conf.SSHServerConfig
	options := make(map[string]json.RawMessage, len(settings))
	for key, value := range settings {
		if key != "clients" {
			options[key] = value
		}
	}
	raw, err := json.Marshal(options)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&native); err != nil {
		return err
	}
	if _, err := native.Build(); err != nil {
		return err
	}
	if err := rejectSSHRuntimeOwnerIDs(inbound.Settings); err != nil {
		return err
	}
	clients, err := ParseInboundSettingsClients(inbound.Settings)
	if err != nil {
		return err
	}
	for i := range clients {
		if strings.TrimSpace(clients[i].Email) == "" {
			return fmt.Errorf("SSH client label is required")
		}
		if err := normalizeSSHCredentials(&clients[i]); err != nil {
			return err
		}
	}
	return nil
}

// Inspect the original wire entries before DTO decoding discards unknown IDs.
func rejectSSHRuntimeOwnerIDs(raw string) error {
	var settings struct {
		Clients []map[string]json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return err
	}
	for _, entry := range settings.Clients {
		for _, name := range []string{"clientId", "client_id"} {
			if _, ok := entry[name]; ok {
				return fmt.Errorf("SSH runtime owner IDs are assigned by SQL")
			}
		}
	}
	return nil
}

func resolveSSHInboundCredentials(tx *gorm.DB, inbound *model.Inbound) error {
	if inbound.Protocol != model.SSH {
		return nil
	}
	clients, err := ParseInboundSettingsClients(inbound.Settings)
	if err != nil {
		return err
	}
	for i := range clients {
		var rows []model.ClientRecord
		if err := tx.Where("email = ?", strings.TrimSpace(clients[i].Email)).Limit(1).Find(&rows).Error; err != nil {
			return err
		}
		var stored *model.ClientRecord
		if len(rows) == 1 {
			stored = &rows[0]
		}
		if err := resolveSSHClientCredentials(&clients[i], stored, true); err != nil {
			return err
		}
	}
	if err := validateSSHListenerAccounts(inbound, clients); err != nil {
		return err
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

func validateSSHBindingChanges(tx *gorm.DB, inbound *model.Inbound, clients []model.Client, detach []string, prune bool) error {
	prospective := make(map[string]model.Client)
	if !prune {
		var stored []model.ClientRecord
		if err := tx.Table("clients c").Select("c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").Where("ci.inbound_id = ?", inbound.Id).Find(&stored).Error; err != nil {
			return err
		}
		for _, row := range stored {
			prospective[row.Email] = *row.ToClient()
		}
	}
	for _, email := range detach {
		delete(prospective, strings.TrimSpace(email))
	}
	seen := make(map[string]bool, len(clients))
	for _, client := range clients {
		email := strings.TrimSpace(client.Email)
		if email == "" || seen[email] {
			return fmt.Errorf("SSH listener requires unique nonempty client labels")
		}
		seen[email] = true
		prospective[email] = client
	}
	all := make([]model.Client, 0, len(prospective))
	for _, client := range prospective {
		all = append(all, client)
	}
	return validateSSHListenerAccounts(inbound, all)
}

func validateLinkedSSHCredentialChanges(tx *gorm.DB, targetID int, clients []model.Client, existing map[string]*model.ClientRecord) error {
	changed := make(map[int]model.Client)
	for _, client := range clients {
		row := existing[strings.TrimSpace(client.Email)]
		if row == nil {
			continue
		}
		if err := resolveSSHClientCredentials(&client, row, false); err != nil {
			return err
		}
		if client.SSHUsername != row.SSHUsername || client.SSHAuthorizedKeys != row.SSHAuthorizedKeys || client.SSHPassword != row.SSHPassword {
			changed[row.Id] = client
		}
	}
	if len(changed) == 0 {
		return nil
	}
	ids := make([]int, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	var listeners []model.Inbound
	if err := tx.Table("inbounds i").Select("i.*").Joins("JOIN client_inbounds ci ON ci.inbound_id = i.id").Where("ci.client_id IN ? AND i.protocol = ? AND i.id <> ?", ids, model.SSH, targetID).Distinct().Find(&listeners).Error; err != nil {
		return err
	}
	for _, listener := range listeners {
		var rows []model.ClientRecord
		if err := tx.Table("clients c").Select("c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").Where("ci.inbound_id = ?", listener.Id).Find(&rows).Error; err != nil {
			return err
		}
		accounts := make([]model.Client, 0, len(rows))
		for _, row := range rows {
			client := *row.ToClient()
			if next, ok := changed[row.Id]; ok {
				client = next
			}
			accounts = append(accounts, client)
		}
		if err := validateSSHListenerAccounts(&listener, accounts); err != nil {
			return err
		}
	}
	return nil
}

// Mirrors contain current authentication only. Explicit clear commands are
// consumed by this transaction and cannot be replayed by a later ordinary edit.
func refreshSSHCredentialMirrors(tx *gorm.DB, targetID int, clientIDs []int) error {
	var listenerIDs []int
	if targetID > 0 {
		listenerIDs = append(listenerIDs, targetID)
	}
	if len(clientIDs) > 0 {
		var linked []int
		if err := tx.Table("client_inbounds ci").Joins("JOIN inbounds i ON i.id = ci.inbound_id").Where("ci.client_id IN ? AND i.protocol = ?", clientIDs, model.SSH).Distinct().Pluck("ci.inbound_id", &linked).Error; err != nil {
			return err
		}
		listenerIDs = append(listenerIDs, linked...)
	}
	slices.Sort(listenerIDs)
	for _, id := range slices.Compact(listenerIDs) {
		var inbound model.Inbound
		if err := tx.First(&inbound, id).Error; err != nil {
			return err
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			return err
		}
		if inbound.Protocol == model.SSH {
			var rows []model.ClientRecord
			if err := tx.Table("clients c").Select("c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").Where("ci.inbound_id = ?", id).Order("c.id").Find(&rows).Error; err != nil {
				return err
			}
			clients := make([]*model.Client, 0, len(rows))
			for _, row := range rows {
				clients = append(clients, row.ToClient())
			}
			var err error
			settings["clients"], err = json.Marshal(clients)
			if err != nil {
				return err
			}
		} else {
			if len(settings["clients"]) == 0 {
				continue
			}
			var clients []map[string]json.RawMessage
			if err := json.Unmarshal(settings["clients"], &clients); err != nil {
				return err
			}
			changed := false
			for _, client := range clients {
				if _, ok := client["clearSshPassword"]; ok {
					delete(client, "clearSshPassword")
					changed = true
				}
				if _, ok := client["clearSshAuthorizedKeys"]; ok {
					delete(client, "clearSshAuthorizedKeys")
					changed = true
				}
			}
			if !changed {
				continue
			}
			var err error
			settings["clients"], err = json.Marshal(clients)
			if err != nil {
				return err
			}
		}
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.Inbound{}).Where("id = ?", id).UpdateColumn("settings", string(raw)).Error; err != nil {
			return err
		}
	}
	return nil
}
