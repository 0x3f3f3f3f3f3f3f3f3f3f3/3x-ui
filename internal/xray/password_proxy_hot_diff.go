package xray

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

type passwordHotCredential struct {
	User     string `json:"user"`
	Pass     string `json:"pass"`
	ClientID string `json:"clientId"`
	Email    string `json:"email"`
}

type passwordHotGroup struct {
	id    string
	users map[string]passwordHotCredential
}

func passwordHotProtocol(protocol string) bool {
	return protocol == "mixed" || protocol == "http"
}

func passwordHotGroups(inbound *InboundConfig) (map[string]*passwordHotGroup, []byte, uint32, error) {
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
		return nil, nil, 0, err
	}
	var mode struct {
		Auth     string `json:"auth"`
		Required bool   `json:"requireAuthentication"`
		Level    uint32 `json:"userLevel"`
	}
	if err := json.Unmarshal(inbound.Settings, &mode); err != nil {
		return nil, nil, 0, err
	}
	if inbound.Protocol == "mixed" && mode.Auth != "password" || inbound.Protocol == "http" && !mode.Required {
		return nil, nil, 0, fmt.Errorf("%w: password hot change requires protected authentication", ErrClientPolicyCapability)
	}
	var accounts []passwordHotCredential
	raw, present := settings["accounts"]
	if !present || string(raw) == "null" {
		return nil, nil, 0, fmt.Errorf("%w: password hot change requires canonical accounts", ErrClientPolicyCapability)
	}
	if err := json.Unmarshal(raw, &accounts); err != nil {
		return nil, nil, 0, err
	}
	groups := make(map[string]*passwordHotGroup)
	usernames := make(map[string]bool)
	emailByID := make(map[string]string)
	for _, account := range accounts {
		if account.User == "" || account.Pass == "" || account.ClientID == "" || account.Email == "" || usernames[account.User] {
			return nil, nil, 0, fmt.Errorf("%w: password hot change has an invalid canonical credential", ErrClientPolicyCapability)
		}
		if email, exists := emailByID[account.ClientID]; exists && email != account.Email {
			return nil, nil, 0, fmt.Errorf("%w: password owner has conflicting email groups", ErrClientPolicyCapability)
		}
		group := groups[account.Email]
		if group == nil {
			group = &passwordHotGroup{id: account.ClientID, users: make(map[string]passwordHotCredential)}
			groups[account.Email] = group
		} else if group.id != account.ClientID {
			return nil, nil, 0, fmt.Errorf("%w: password email has conflicting owners", ErrClientPolicyCapability)
		}
		usernames[account.User] = true
		emailByID[account.ClientID] = account.Email
		group.users[account.User] = account
	}
	delete(settings, "accounts")
	rest, err := json.Marshal(settings)
	return groups, rest, mode.Level, err
}

func diffManagedPasswordUsers(oldIb, newIb *InboundConfig, diff *HotDiff) (bool, error) {
	if oldIb.Port != newIb.Port || oldIb.Tag != newIb.Tag ||
		!rawEqualNormalized(oldIb.Listen, newIb.Listen) ||
		!rawEqualNormalized(oldIb.StreamSettings, newIb.StreamSettings) ||
		!rawEqualNormalized(oldIb.Sniffing, newIb.Sniffing) {
		return false, nil
	}
	oldGroups, oldRest, _, err := passwordHotGroups(oldIb)
	if err != nil {
		return false, err
	}
	newGroups, newRest, level, err := passwordHotGroups(newIb)
	if err != nil {
		return false, err
	}
	if !rawEqualNormalized(oldRest, newRest) {
		return false, nil
	}
	emails := make(map[string]bool)
	for email := range oldGroups {
		emails[email] = true
	}
	for email := range newGroups {
		emails[email] = true
	}
	for _, email := range slices.Sorted(maps.Keys(emails)) {
		old, next := oldGroups[email], newGroups[email]
		if old != nil && next != nil && old.id == next.id && maps.Equal(old.users, next.users) {
			continue
		}
		if old != nil {
			diff.RemovedUsers = append(diff.RemovedUsers, UserOp{Tag: oldIb.Tag, Protocol: oldIb.Protocol, Email: email})
		}
		if next != nil {
			for _, username := range slices.Sorted(maps.Keys(next.users)) {
				account := next.users[username]
				user := map[string]any{"user": account.User, "pass": account.Pass, "clientId": account.ClientID, "email": account.Email, "level": level}
				diff.AddedUsers = append(diff.AddedUsers, UserOp{Tag: newIb.Tag, Protocol: newIb.Protocol, Email: email, User: user})
			}
		}
	}
	return true, nil
}
