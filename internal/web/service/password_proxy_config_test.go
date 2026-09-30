package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyConfigUsesCanonicalOwners(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			first, second := passwordOwner(t, "config-first@example.test"), passwordOwner(t, "config-second@example.test")
			usage := xray.ClientTraffic{Email: first.Email, Enable: true, Up: 123, Down: 456}
			if err := database.GetDB().Create(&usage).Error; err != nil {
				t.Fatal(err)
			}
			accounts := []map[string]any{
				{"user": "alice", "pass": "resource-A", "ownerClientId": first.StableID},
				{"user": "ALICE", "pass": "resource-alias", "ownerClientId": first.StableID},
				{"user": "用户", "pass": "resource-B", "ownerClientId": second.StableID},
			}
			ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 24551, Settings: passwordOwnerSettings(t, protocol, accounts...)})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(ib).Update("enable", true).Error; err != nil {
				t.Fatal(err)
			}
			compiled, err := (&XrayService{}).compileManagedXrayConfig(policyConfigState(t))
			if err != nil {
				t.Fatalf("canonical password configuration: %v", err)
			}
			if len(compiled.config.InboundConfigs) != 1 || len(compiled.state.Policies) != 2 {
				t.Fatalf("listeners/policies = %d/%d", len(compiled.config.InboundConfigs), len(compiled.state.Policies))
			}
			var settings map[string]any
			if err := json.Unmarshal(compiled.config.InboundConfigs[0].Settings, &settings); err != nil {
				t.Fatal(err)
			}
			if _, exists := settings["clients"]; exists {
				t.Fatal("generic shared credentials leaked into password runtime settings")
			}
			want := []any{
				map[string]any{"user": "alice", "pass": "resource-A", "clientId": first.StableID, "email": first.Email},
				map[string]any{"user": "ALICE", "pass": "resource-alias", "clientId": first.StableID, "email": first.Email},
				map[string]any{"user": "用户", "pass": "resource-B", "clientId": second.StableID, "email": second.Email},
			}
			if !reflect.DeepEqual(settings["accounts"], want) {
				t.Fatal("runtime accounts differ from canonical owner/resource credentials")
			}
			if protocol == model.HTTP && settings["requireAuthentication"] != true {
				t.Fatal("managed HTTP lost required authentication")
			}
			for _, prior := range []model.ClientRecord{first, second} {
				var got model.ClientRecord
				if err := database.GetDB().First(&got, prior.Id).Error; err != nil || !reflect.DeepEqual(got, prior) {
					t.Fatalf("compilation changed canonical record: %v", err)
				}
			}
			var stored model.Inbound
			if err := database.GetDB().First(&stored, ib.Id).Error; err != nil || stored.Settings != ib.Settings {
				t.Fatalf("compilation changed stored resource settings: %v", err)
			}
			if err := database.GetDB().First(&usage, usage.Id).Error; err != nil || usage.Up != 123 || usage.Down != 456 {
				t.Fatalf("compilation changed historical usage: %v", err)
			}
		})
	}
}

func TestPasswordProxyConfigProtectedEmptyListener(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			owner := passwordOwner(t, "empty-password-owner@example.test")
			ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 24553, Settings: passwordOwnerSettings(t, protocol, map[string]any{"user": "alice", "pass": "resource", "ownerClientId": owner.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			edited := *ib
			edited.Settings = passwordOwnerSettings(t, protocol)
			if _, _, err := (&InboundService{}).UpdateInbound(&edited); err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(ib).Update("enable", true).Error; err != nil {
				t.Fatal(err)
			}
			compiled, err := (&XrayService{}).compileManagedXrayConfig(policyConfigState(t))
			if err != nil {
				t.Fatal(err)
			}
			if len(compiled.state.Policies) != 0 || len(compiled.records) != 0 {
				t.Fatal("empty password listener invented an owner")
			}
			var settings map[string]any
			if err := json.Unmarshal(compiled.config.InboundConfigs[0].Settings, &settings); err != nil {
				t.Fatal(err)
			}
			if accounts, ok := settings["accounts"].([]any); !ok || len(accounts) != 0 {
				t.Fatal("empty password listener retained credentials")
			}
			if protocol == model.Mixed && settings["auth"] != "password" || protocol == model.HTTP && settings["requireAuthentication"] != true {
				t.Fatal("empty password listener became anonymous")
			}
		})
	}
}

func TestPasswordProxyConfigRejectsUnownedAccounts(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			mkInbound(t, 24554, protocol, passwordOwnerSettings(t, protocol, map[string]any{"user": "legacy", "pass": "resource"}))
			if _, err := (&XrayService{}).compileManagedXrayConfig(policyConfigState(t)); !errors.Is(err, xray.ErrClientPolicyCapability) {
				t.Fatalf("unowned password listener entered managed mode: %v", err)
			}
		})
	}
}

func TestPasswordProxyConfigRestoredHTTPProtection(t *testing.T) {
	for _, owned := range []bool{false, true} {
		for _, marker := range []string{"omitted", "false"} {
			t.Run(fmt.Sprintf("owned=%t/%s", owned, marker), func(t *testing.T) {
				setupPolicyLedgerDB(t)
				owner := passwordOwner(t, "restored-http-owner")
				account := map[string]any{"user": "alice", "pass": "resource"}
				if owned {
					account["ownerClientId"] = owner.StableID
				}
				svc := &InboundService{}
				ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24555, Settings: passwordOwnerSettings(t, model.HTTP, account)})
				if err != nil {
					t.Fatal(err)
				}
				var restored map[string]any
				if err := json.Unmarshal([]byte(ib.Settings), &restored); err != nil {
					t.Fatal(err)
				}
				delete(restored, "requireAuthentication")
				if marker == "false" {
					restored["requireAuthentication"] = false
				}
				raw, err := json.Marshal(restored)
				if err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Model(ib).Update("settings", string(raw)).Error; err != nil {
					t.Fatal(err)
				}
				edited := *ib
				edited.Settings = passwordOwnerSettings(t, model.HTTP)
				if _, _, err := svc.UpdateInbound(&edited); err != nil {
					t.Fatal(err)
				}
				var saved map[string]any
				if err := json.Unmarshal([]byte(edited.Settings), &saved); err != nil {
					t.Fatal(err)
				}
				if protected := saved["requireAuthentication"] == true; protected != owned {
					t.Fatalf("empty restored HTTP protection = %t, owned=%t", protected, owned)
				}
			})
		}
	}
}

func TestPasswordProxyConfigDisabledOwnersKeepAuthentication(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			owner := passwordOwner(t, "disabled-password-owner@example.test")
			ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 24552, Settings: passwordOwnerSettings(t, protocol, map[string]any{"user": "alice", "pass": "resource", "ownerClientId": owner.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(ib).Update("enable", true).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(&owner).Update("enable", false).Error; err != nil {
				t.Fatal(err)
			}
			compiled, err := (&XrayService{}).compileManagedXrayConfig(policyConfigState(t))
			if err != nil {
				t.Fatal(err)
			}
			if len(compiled.state.Policies) != 1 || compiled.state.Policies[0].Enabled {
				t.Fatal("disabled owner policy was omitted or enabled")
			}
			var settings map[string]any
			if err := json.Unmarshal(compiled.config.InboundConfigs[0].Settings, &settings); err != nil {
				t.Fatal(err)
			}
			if accounts, ok := settings["accounts"].([]any); !ok || len(accounts) != 0 {
				t.Fatal("disabled owner credentials remained available")
			}
			if protocol == model.Mixed && settings["auth"] != "password" || protocol == model.HTTP && settings["requireAuthentication"] != true {
				t.Fatal("empty managed password listener became anonymous")
			}
		})
	}
}

func TestPasswordProxyConfigSnapshotPreservesOwnerAndCredential(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	first, second := passwordOwner(t, "snapshot-password-first"), passwordOwner(t, "snapshot-password-second")
	ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24556, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "resource-A", "ownerClientId": first.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	if err := db.Model(ib).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	const callback = "test:password-reassign-after-listener-read"
	reassigned := false
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if reassigned || tx.Statement.Table != "inbounds" {
			return
		}
		reassigned = true
		if err := db.Transaction(func(change *gorm.DB) error {
			settings := passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "resource-B", "ownerClientId": second.StableID})
			if err := change.Model(ib).Update("settings", settings).Error; err != nil {
				return err
			}
			if err := change.Where("inbound_id = ?", ib.Id).Delete(&model.ClientInbound{}).Error; err != nil {
				return err
			}
			return change.Create(&model.ClientInbound{ClientId: second.Id, InboundId: ib.Id}).Error
		}); err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	compiled, err := (&XrayService{}).compileManagedXrayConfig(policyConfigState(t))
	if err != nil || !reassigned {
		t.Fatalf("snapshot compilation: changed=%t error=%v", reassigned, err)
	}
	readAccount := func(candidate *compiledManagedConfig) (string, string) {
		t.Helper()
		var settings struct {
			Accounts []struct {
				ClientID string `json:"clientId"`
				Pass     string `json:"pass"`
			} `json:"accounts"`
		}
		if len(candidate.config.InboundConfigs) != 1 || json.Unmarshal(candidate.config.InboundConfigs[0].Settings, &settings) != nil || len(settings.Accounts) != 1 {
			t.Fatal("snapshot candidate lost its account")
		}
		return settings.Accounts[0].ClientID, settings.Accounts[0].Pass
	}
	id, password := readAccount(compiled)
	oldSnapshot := id == first.StableID && password == "resource-A"
	newSnapshot := id == second.StableID && password == "resource-B"
	if !oldSnapshot && !newSnapshot {
		t.Fatal("snapshot mixed a resource credential with a different canonical owner")
	}
	if len(compiled.records) != 1 || compiled.records[id].StableID != id {
		t.Fatal("snapshot policy and authenticated owner disagree")
	}
	latest, err := (&XrayService{}).compileManagedXrayConfig(policyConfigState(t))
	if err != nil {
		t.Fatal(err)
	}
	id, password = readAccount(latest)
	if id != second.StableID || password != "resource-B" {
		t.Fatal("compiler retained the previous snapshot after reassignment committed")
	}
}

func TestPasswordProxyConfigRejectsStaleMembershipBeforePreparation(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	owner := passwordOwner(t, "stale-password-config")
	ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24557, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "resource", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(ib).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Where("inbound_id = ?", ib.Id).Delete(&model.ClientInbound{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&XrayService{}).GetManagedXrayConfig(policyConfigState(t)); !errors.Is(err, ErrPasswordProxyOwner) {
		t.Fatalf("stale password membership entered configuration: %v", err)
	}
	var saved model.ClientRecord
	if err := database.GetDB().First(&saved, owner.Id).Error; err != nil || saved.DesiredPolicyVersion != 0 {
		t.Fatalf("rejected candidate prepared policy state: version=%d error=%v", saved.DesiredPolicyVersion, err)
	}
}

func TestPasswordProxyConfigRejectsAnonymousResources(t *testing.T) {
	for _, tc := range []struct {
		protocol model.Protocol
		settings string
	}{
		{model.Mixed, `{"auth":"noauth","accounts":[]}`},
		{model.Mixed, `{"auth":"noauth","accounts":[{"user":"legacy","pass":"resource"}]}`},
		{model.HTTP, `{"accounts":[]}`},
		{model.HTTP, `{"accounts":[],"requireAuthentication":false}`},
	} {
		t.Run(string(tc.protocol)+tc.settings, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			mkInbound(t, 24558, tc.protocol, tc.settings)
			if _, err := (&XrayService{}).compileManagedXrayConfig(policyConfigState(t)); !errors.Is(err, xray.ErrClientPolicyCapability) {
				t.Fatalf("anonymous resource entered managed mode: %v", err)
			}
		})
	}
}

func TestPasswordProxyConfigNormalizesActiveAliasAndOmitsDormantUsers(t *testing.T) {
	for _, activeAlias := range []bool{false, true} {
		t.Run(fmt.Sprintf("active-users=%t", activeAlias), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			owner := passwordOwner(t, "alias-password-config")
			account := map[string]any{"User": "用户", "Pass": "resource", "OwnerClientId": owner.StableID}
			ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24559, Settings: passwordOwnerSettings(t, model.HTTP, account)})
			if err != nil {
				t.Fatal(err)
			}
			restored := map[string]any{"Accounts": []any{account}, "USERS": []any{map[string]any{"user": "dormant", "pass": "legacy"}}}
			if activeAlias {
				restored["Accounts"], restored["USERS"] = nil, []any{account}
			}
			raw, err := json.Marshal(restored)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(ib).Updates(map[string]any{"settings": string(raw), "enable": true}).Error; err != nil {
				t.Fatal(err)
			}
			compiled, err := (&XrayService{}).compileManagedXrayConfig(policyConfigState(t))
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]any
			if err := json.Unmarshal(compiled.config.InboundConfigs[0].Settings, &settings); err != nil {
				t.Fatal(err)
			}
			if _, exists := settings["users"]; exists {
				t.Fatal("runtime retained a dormant or alternate credential array")
			}
			want := []any{map[string]any{"user": "用户", "pass": "resource", "clientId": owner.StableID, "email": owner.Email}}
			if !reflect.DeepEqual(settings["accounts"], want) {
				t.Fatal("runtime alias lost canonical identity or resource credential")
			}
		})
	}
}
