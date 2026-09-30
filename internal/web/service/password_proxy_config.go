package service

import (
	"encoding/json"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Resource credentials and canonical owners come from the same read snapshot.
// Global UUID/password fields never supply password-proxy wire credentials.
func bindManagedPasswordProxyIdentity(inbound *xray.InboundConfig, records []model.ClientRecord) error {
	source := model.Inbound{Protocol: model.Protocol(inbound.Protocol), Settings: string(inbound.Settings)}
	if err := preparePasswordProxyOwnerCommand(&source); err != nil {
		return err
	}
	settings, accounts, err := passwordProxyAccounts(&source)
	if err != nil {
		return err
	}
	if source.Protocol == model.Mixed {
		var auth string
		if err := json.Unmarshal(settings["auth"], &auth); err != nil || auth != "password" {
			return fmt.Errorf("%w: managed Mixed requires password authentication", xray.ErrClientPolicyCapability)
		}
	} else {
		var required bool
		if raw := settings["requireAuthentication"]; len(raw) != 0 {
			if err := json.Unmarshal(raw, &required); err != nil {
				return err
			}
		}
		if !required {
			return fmt.Errorf("%w: managed HTTP requires protected authentication", xray.ErrClientPolicyCapability)
		}
	}
	byID := make(map[string]model.ClientRecord, len(records))
	for _, record := range records {
		if record.StableID == "" || record.Email == "" {
			return fmt.Errorf("%w: password account has no canonical owner", xray.ErrClientPolicyCapability)
		}
		if _, duplicate := byID[record.StableID]; duplicate {
			return ErrManagedConfigStale
		}
		byID[record.StableID] = record
	}
	var entries []map[string]json.RawMessage
	if raw := settings["accounts"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(records))
	active := make([]map[string]json.RawMessage, 0, len(accounts))
	for i, account := range accounts {
		if account.OwnerClientID == "" {
			return fmt.Errorf("%w: password credential has no canonical owner", xray.ErrClientPolicyCapability)
		}
		owner, exists := byID[account.OwnerClientID]
		if !exists {
			return ErrManagedConfigStale
		}
		seen[owner.StableID] = true
		// Validate ownership before filtering disabled credentials.
		if !owner.Enable {
			continue
		}
		entry := entries[i]
		delete(entry, "ownerClientId")
		entry["clientId"], err = json.Marshal(owner.StableID)
		if err != nil {
			return err
		}
		entry["email"], err = json.Marshal(owner.Email)
		if err != nil {
			return err
		}
		active = append(active, entry)
	}
	if len(seen) != len(byID) {
		return ErrManagedConfigStale
	}
	settings["accounts"], err = json.Marshal(active)
	if err != nil {
		return err
	}
	delete(settings, "users")
	delete(settings, "clients")
	delete(settings, "clientId")
	delete(settings, "client_id")
	inbound.Settings, err = json.Marshal(settings)
	return err
}
