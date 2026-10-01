package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/mieru"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func validateProvidedMieruCredentials(client model.Client) error {
	for _, value := range []string{client.MieruUsername, client.MieruPassword} {
		if len(value) > 64 || !utf8.ValidString(value) {
			return fmt.Errorf("mieru credentials must be valid UTF-8 and at most 64 bytes")
		}
	}
	return nil
}

func fillMieruCredentials(client *model.Client) error {
	if client.MieruUsername == "" {
		client.MieruUsername = "mieru-" + uuid.NewString()
	}
	if client.MieruPassword == "" {
		client.MieruPassword = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return mieru.ValidateCredentials(client.MieruUsername, client.MieruPassword)
}

func prepareMieruInbound(inbound *model.Inbound) error {
	if inbound.Protocol != model.Mieru {
		return nil
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return err
	}
	if settings == nil {
		return fmt.Errorf("mieru settings must be an object")
	}
	allowed := map[string]bool{"clients": true, "transport": true, "mtu": true, "userHintRequired": true, "maxConnections": true, "handshakeTimeoutSeconds": true}
	for key := range settings {
		if !allowed[key] {
			return fmt.Errorf("unsupported panel mieru setting %q", key)
		}
	}
	// Validate the physical listener and original stream before panel normalization
	// can discard an unsupported wrapper. Identities are filled from SQL later.
	candidate := *inbound
	native := make(map[string]json.RawMessage, len(settings))
	for key, value := range settings {
		if key != "clients" {
			native[key] = value
		}
	}
	native["users"] = json.RawMessage(`[]`)
	raw, err := json.Marshal(native)
	if err != nil {
		return err
	}
	candidate.Settings = string(raw)
	raw, err = json.Marshal(candidate.GenXrayInboundConfig())
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
	for i := range clients {
		if strings.TrimSpace(clients[i].Email) == "" {
			return fmt.Errorf("mieru client label is required")
		}
		var stored model.ClientRecord
		err := database.GetDB().Where("email = ?", clients[i].Email).First(&stored).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if clients[i].MieruUsername == "" {
			clients[i].MieruUsername = stored.MieruUsername
		}
		if clients[i].MieruPassword == "" {
			clients[i].MieruPassword = stored.MieruPassword
		}
		if err := fillMieruCredentials(&clients[i]); err != nil {
			return err
		}
	}
	if clients == nil {
		clients = []model.Client{}
	}
	settings["clients"], err = json.Marshal(clients)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(settings)
	if err == nil {
		inbound.Settings = string(raw)
	}
	return err
}

// Include disabled bindings: enabling an account must not change who a wire
// username authenticates. Check the complete prospective listener before writes.
func validateMieruBindingChanges(tx *gorm.DB, inboundID int, clients []model.Client, existing map[string]*model.ClientRecord, detach []string, prune bool) error {
	prospective := make(map[string]model.Client)
	if !prune {
		var stored []model.ClientRecord
		if err := tx.Table("clients c").Select("c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").Where("ci.inbound_id = ?", inboundID).Find(&stored).Error; err != nil {
			return err
		}
		for _, record := range stored {
			prospective[record.Email] = *record.ToClient()
		}
	}
	for _, email := range detach {
		delete(prospective, strings.TrimSpace(email))
	}
	seenEmails := make(map[string]bool, len(clients))
	for _, client := range clients {
		email := strings.TrimSpace(client.Email)
		if email == "" || seenEmails[email] {
			return fmt.Errorf("mieru listener requires unique nonempty client labels")
		}
		seenEmails[email] = true
		if record := existing[email]; record != nil {
			if client.MieruUsername == "" {
				client.MieruUsername = record.MieruUsername
			}
			if client.MieruPassword == "" {
				client.MieruPassword = record.MieruPassword
			}
		}
		prospective[email] = client
	}
	seen := make(map[string]bool, len(prospective))
	for _, client := range prospective {
		if err := mieru.ValidateCredentials(client.MieruUsername, client.MieruPassword); err != nil {
			return err
		}
		if seen[client.MieruUsername] {
			return fmt.Errorf("mieru username belongs to multiple listener clients")
		}
		seen[client.MieruUsername] = true
	}
	return nil
}

// Native credentials live on the canonical record. A write through any protocol
// can affect every linked mieru listener, including disabled reserved bindings.
func validateLinkedMieruCredentialChanges(tx *gorm.DB, targetID int, clients []model.Client, existing map[string]*model.ClientRecord) error {
	changed := make(map[int]model.Client)
	for _, client := range clients {
		record := existing[strings.TrimSpace(client.Email)]
		if record == nil {
			continue
		}
		if client.MieruUsername == "" {
			client.MieruUsername = record.MieruUsername
		}
		if client.MieruPassword == "" {
			client.MieruPassword = record.MieruPassword
		}
		if client.MieruUsername != record.MieruUsername || client.MieruPassword != record.MieruPassword {
			changed[record.Id] = client
		}
	}
	if len(changed) == 0 {
		return nil
	}
	ids := make([]int, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	var listenerIDs []int
	if err := tx.Table("client_inbounds ci").Joins("JOIN inbounds i ON i.id = ci.inbound_id").Where("ci.client_id IN ? AND i.protocol = ? AND i.id <> ?", ids, model.Mieru, targetID).Distinct().Pluck("ci.inbound_id", &listenerIDs).Error; err != nil {
		return err
	}
	for _, id := range listenerIDs {
		var rows []model.ClientRecord
		if err := tx.Table("clients c").Select("c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").Where("ci.inbound_id = ?", id).Find(&rows).Error; err != nil {
			return err
		}
		seen := make(map[string]bool, len(rows))
		for _, row := range rows {
			username, password := row.MieruUsername, row.MieruPassword
			if next, ok := changed[row.Id]; ok {
				username, password = next.MieruUsername, next.MieruPassword
			}
			if err := mieru.ValidateCredentials(username, password); err != nil {
				return err
			}
			if seen[username] {
				return fmt.Errorf("mieru username belongs to multiple clients on linked listener %d", id)
			}
			seen[username] = true
		}
	}
	return nil
}

func bindManagedMieruIdentity(inbound *xray.InboundConfig, records []model.ClientRecord) error {
	var settings map[string]any
	if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
		return err
	}
	if settings == nil {
		return fmt.Errorf("%w: mieru settings required", xray.ErrClientPolicyCapability)
	}
	byEmail := make(map[string]model.ClientRecord, len(records))
	for _, record := range records {
		if _, exists := byEmail[record.Email]; exists || record.StableID == "" {
			return fmt.Errorf("%w: mieru identity is not unique", ErrManagedConfigStale)
		}
		byEmail[record.Email] = record
	}
	users, ok := settings["users"].([]any)
	if !ok || len(users) != len(records) {
		return fmt.Errorf("%w: mieru account membership changed", ErrManagedConfigStale)
	}
	seen := make(map[string]bool, len(users))
	for _, raw := range users {
		user, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: invalid mieru account", ErrManagedConfigStale)
		}
		email, _ := user["email"].(string)
		record, exists := byEmail[email]
		username, _ := user["username"].(string)
		password, _ := user["password"].(string)
		if !exists || seen[email] || username != record.MieruUsername || password != record.MieruPassword {
			return fmt.Errorf("%w: mieru authenticated account changed", ErrManagedConfigStale)
		}
		if err := mieru.ValidateCredentials(username, password); err != nil {
			return err
		}
		seen[email] = true
		delete(user, "client_id")
		user["clientId"] = record.StableID
	}
	delete(settings, "clients")
	delete(settings, "clientId")
	delete(settings, "client_id")
	var err error
	inbound.Settings, err = json.Marshal(settings)
	return err
}
