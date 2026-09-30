package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var ErrPasswordProxyOwner = errors.New("invalid password proxy account ownership")

type passwordProxyAccount struct {
	User          string `json:"user"`
	Pass          string `json:"pass"`
	OwnerClientID string `json:"ownerClientId,omitempty"`
}

func isPasswordProxy(protocol model.Protocol) bool {
	return protocol == model.Mixed || protocol == model.HTTP
}

// Match the native JSON decoder's case folding, while refusing conflicting
// spellings instead of letting map iteration choose an authentication identity.
func canonicalPasswordFields(raw map[string]json.RawMessage, keys []string) (map[string]json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	out := make(map[string]json.RawMessage, len(raw))
	seen := make(map[string]bool)
	for key, value := range raw {
		canonical := key
		for _, known := range keys {
			if strings.EqualFold(key, known) {
				canonical = known
				break
			}
		}
		if seen[canonical] {
			return nil, fmt.Errorf("%w: ambiguous case variants for %s", ErrPasswordProxyOwner, canonical)
		}
		seen[canonical] = true
		out[canonical] = value
	}
	return out, nil
}

func passwordProxyAccounts(inbound *model.Inbound) (map[string]json.RawMessage, []passwordProxyAccount, error) {
	var rawSettings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &rawSettings); err != nil {
		return nil, nil, err
	}
	if rawSettings == nil {
		return nil, nil, fmt.Errorf("%w: settings must be an object", ErrPasswordProxyOwner)
	}
	settings, err := canonicalPasswordFields(rawSettings, []string{"accounts", "users", "auth", "clients", "requireAuthentication"})
	if err != nil {
		return nil, nil, err
	}
	active := "users"
	for _, field := range []string{"accounts", "users"} {
		raw, exists := settings[field]
		if !exists {
			continue
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, nil, err
		}
		// Native Accounts != nil overrides Users, including an explicitly empty [].
		if field == "accounts" && entries != nil {
			active = "accounts"
		}
		for i, entry := range entries {
			normalized, err := canonicalPasswordFields(entry, []string{"user", "pass", "ownerClientId", "clientId", "client_id", "email"})
			if err != nil {
				return nil, nil, err
			}
			entries[i] = normalized
		}
		normalized, err := json.Marshal(entries)
		if err != nil {
			return nil, nil, err
		}
		settings[field] = normalized
	}
	var accounts []passwordProxyAccount
	if raw := settings[active]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &accounts); err != nil {
			return nil, nil, err
		}
	}
	if active == "users" {
		// Canonicalize an active alias so the existing accounts form preserves it.
		if raw, exists := settings["users"]; exists {
			settings["accounts"] = raw
			delete(settings, "users")
		}
	} else if raw := settings["users"]; len(raw) != 0 {
		var dormant []passwordProxyAccount
		if err := json.Unmarshal(raw, &dormant); err != nil {
			return nil, nil, err
		}
		for _, account := range dormant {
			if account.OwnerClientID != "" {
				return nil, nil, fmt.Errorf("%w: dormant users cannot select an owner", ErrPasswordProxyOwner)
			}
		}
	}
	return settings, accounts, nil
}

func preparePasswordProxyOwnerCommand(inbound *model.Inbound) error {
	if !isPasswordProxy(inbound.Protocol) {
		return nil
	}
	settings, accounts, err := passwordProxyAccounts(inbound)
	if err != nil {
		return err
	}
	owned := false
	for _, account := range accounts {
		owned = owned || account.OwnerClientID != ""
	}
	if owned {
		if inbound.NodeID != nil {
			return fmt.Errorf("%w: accounts require a local listener", ErrPasswordProxyOwner)
		}
		if len(inbound.ClientStats) != 0 {
			return fmt.Errorf("%w: account owners cannot supply clientStats", ErrPasswordProxyOwner)
		}
		if clients, err := ParseInboundSettingsClients(inbound.Settings); err != nil {
			return err
		} else if len(clients) != 0 {
			return fmt.Errorf("%w: use account owners instead of settings.clients", ErrPasswordProxyOwner)
		}
		if inbound.Protocol == model.Mixed {
			var auth string
			if err := json.Unmarshal(settings["auth"], &auth); err != nil || auth != "password" {
				return fmt.Errorf("%w: owned Mixed accounts require password authentication", ErrPasswordProxyOwner)
			}
		} else {
			settings["requireAuthentication"] = json.RawMessage("true")
		}
		names := make(map[string]bool, len(accounts))
		for _, account := range accounts {
			if account.User == "" || account.Pass == "" || account.OwnerClientID == "" {
				return fmt.Errorf("%w: every credential requires a username, password and owner", ErrPasswordProxyOwner)
			}
			if id, err := uuid.Parse(account.OwnerClientID); err != nil || id.String() != account.OwnerClientID {
				return fmt.Errorf("%w: ownerClientId requires a canonical stable UUID", ErrPasswordProxyOwner)
			}
			if names[account.User] {
				return fmt.Errorf("%w: duplicate managed username", ErrPasswordProxyOwner)
			}
			names[account.User] = true
		}
	}
	// Core identity is always generated from canonical records, never caller metadata.
	for _, field := range []string{"accounts", "users"} {
		raw, exists := settings[field]
		if !exists {
			continue
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return err
		}
		for _, account := range entries {
			delete(account, "clientId")
			delete(account, "client_id")
			delete(account, "email")
		}
		raw, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		settings[field] = raw
	}

	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	inbound.Settings = string(raw)
	return nil
}

func resolvePasswordProxyOwners(tx *gorm.DB, inbound *model.Inbound) ([]model.ClientRecord, error) {
	if !isPasswordProxy(inbound.Protocol) {
		return nil, nil
	}
	if inbound.Id != 0 {
		var stored model.Inbound
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&stored, inbound.Id).Error; err != nil {
			return nil, err
		}
		if err := validatePasswordProxyOwnerBindings(tx, &stored); err != nil {
			return nil, err
		}
		// Once protected, clearing the last account does not enable anonymous HTTP.
		if inbound.Protocol == model.HTTP && isPasswordProxy(stored.Protocol) {
			prior, priorAccounts, err := passwordProxyAccounts(&stored)
			if err != nil {
				return nil, err
			}
			var required bool
			if raw := prior["requireAuthentication"]; len(raw) != 0 {
				if err := json.Unmarshal(raw, &required); err != nil {
					return nil, err
				}
			}
			if stored.Protocol == model.Mixed {
				var auth string
				if raw := prior["auth"]; len(raw) != 0 {
					if err := json.Unmarshal(raw, &auth); err != nil {
						return nil, err
					}
				}
				owned := slices.ContainsFunc(priorAccounts, func(account passwordProxyAccount) bool { return account.OwnerClientID != "" })
				required = owned && auth == "password"
			}
			if required {
				settings, _, err := passwordProxyAccounts(inbound)
				if err != nil {
					return nil, err
				}
				settings["requireAuthentication"] = json.RawMessage("true")
				raw, err := json.Marshal(settings)
				if err != nil {
					return nil, err
				}
				inbound.Settings = string(raw)
			}
		}
	}
	_, accounts, err := passwordProxyAccounts(inbound)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if account.OwnerClientID != "" && !seen[account.OwnerClientID] {
			seen[account.OwnerClientID] = true
			ids = append(ids, account.OwnerClientID)
		}
	}
	slices.Sort(ids)
	owners := make([]model.ClientRecord, 0, len(ids))
	for _, batch := range chunkStrings(ids, 400) {
		var rows []model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", batch).Order("stable_id").Find(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) != len(batch) {
			return nil, fmt.Errorf("%w: owner does not exist", ErrPasswordProxyOwner)
		}
		owners = append(owners, rows...)
	}
	if len(ids) != 0 {
		if err := validateLocalClientPolicyResetScope(tx, ids); err != nil {
			return nil, err
		}
	}
	return owners, nil
}

func (s *ClientService) syncPasswordProxyOwnerLinks(tx *gorm.DB, inbound *model.Inbound, owners []model.ClientRecord) error {
	var old []model.ClientRecord
	if err := tx.Table("clients c").Select("c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").Where("ci.inbound_id = ?", inbound.Id).Find(&old).Error; err != nil {
		return err
	}
	wanted := make(map[int]string, len(owners))
	ids := make([]int, 0, len(owners))
	for _, owner := range owners {
		wanted[owner.Id] = ""
		ids = append(ids, owner.Id)
		// Existing shared statistics are authoritative; account edits cannot overwrite them.
		client := owner.ToClient()
		stat := xray.ClientTraffic{Email: owner.Email, InboundId: inbound.Id, Enable: owner.Enable, Total: client.TotalGB, ExpiryTime: client.ExpiryTime, Reset: client.Reset, ResetDay: client.ResetDay, ResetWeekday: client.ResetWeekday, ResetMax: client.ResetMax}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "email"}}, DoNothing: true}).Create(&stat).Error; err != nil {
			return err
		}
	}
	if err := s.reconcileInboundLinks(tx, inbound.Id, wanted, ids, nil, true); err != nil {
		return err
	}
	for _, owner := range old {
		if _, keep := wanted[owner.Id]; keep {
			continue
		}
		if _, err := preserveDetachedTunnelOwnerTraffic(tx, inbound.Id, owner.Email); err != nil {
			return err
		}
	}
	return validatePasswordProxyOwnerBindings(tx, inbound)
}

func validatePasswordProxyOwnerBindings(tx *gorm.DB, inbound *model.Inbound) error {
	if !isPasswordProxy(inbound.Protocol) {
		return nil
	}
	if err := validateTunnelSourceACLConfig(inbound); err != nil {
		return err
	}
	checked := *inbound
	checked.ClientStats = nil // Preloaded SQL rows are not command-supplied mirrors.
	if err := preparePasswordProxyOwnerCommand(&checked); err != nil {
		return err
	}
	settings, accounts, err := passwordProxyAccounts(inbound)
	if err != nil {
		return err
	}
	for _, field := range []string{"accounts", "users"} {
		var entries []map[string]json.RawMessage
		if raw := settings[field]; len(raw) != 0 {
			if err := json.Unmarshal(raw, &entries); err != nil {
				return err
			}
		}
		for _, account := range entries {
			// Bare legacy email is ignored by the native validator without a client ID.
			for _, key := range []string{"clientId", "client_id"} {
				if _, exists := account[key]; exists {
					return fmt.Errorf("%w: saved account contains untrusted core identity", ErrPasswordProxyOwner)
				}
			}
		}
	}

	wanted := make(map[string]bool)
	for _, account := range accounts {
		if account.OwnerClientID != "" {
			wanted[account.OwnerClientID] = true
		}
	}
	var rows []struct{ StableID string }
	if err := tx.Table("client_inbounds ci").Select("c.stable_id").Joins("LEFT JOIN clients c ON c.id = ci.client_id").Where("ci.inbound_id = ?", inbound.Id).Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(wanted) {
		return fmt.Errorf("%w: account ownership differs from canonical memberships", ErrPasswordProxyOwner)
	}
	for _, row := range rows {
		if !wanted[row.StableID] {
			return fmt.Errorf("%w: account owner has no canonical membership", ErrPasswordProxyOwner)
		}
	}
	return nil
}

func validatePasswordProxyInbounds(tx *gorm.DB, inbounds []*model.Inbound) error {
	for _, inbound := range inbounds {
		if err := validatePasswordProxyOwnerBindings(tx, inbound); err != nil {
			return err
		}
	}
	return nil
}

// Until canonical runtime generation is verified, an owned account cannot enter
// an unmanaged data path that would ignore its panel ownership metadata.
func guardUnmanagedPasswordProxy(inbound *model.Inbound) error {
	if !isPasswordProxy(inbound.Protocol) {
		return nil
	}
	_, accounts, err := passwordProxyAccounts(inbound)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if account.OwnerClientID != "" {
			return fmt.Errorf("%w: canonical password account activation is not implemented", xray.ErrClientPolicyCapability)
		}
	}
	return nil
}

func preserveRemovedPasswordProxyHistory(tx *gorm.DB, inboundID int, prior []model.ClientRecord) error {
	if len(prior) == 0 {
		return nil
	}
	var links []model.ClientInbound
	if err := tx.Where("inbound_id = ?", inboundID).Find(&links).Error; err != nil {
		return err
	}
	kept := make(map[int]bool, len(links))
	for _, link := range links {
		kept[link.ClientId] = true
	}
	for _, owner := range prior {
		if !kept[owner.Id] {
			if _, err := preserveDetachedTunnelOwnerTraffic(tx, inboundID, owner.Email); err != nil {
				return err
			}
		}
	}
	return nil
}
