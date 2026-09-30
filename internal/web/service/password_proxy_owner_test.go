package service

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func passwordOwner(t *testing.T, email string) model.ClientRecord {
	t.Helper()
	row := model.ClientRecord{Email: email, Enable: true, UUID: uuid.NewString(), Password: "shared-original", SubID: uuid.NewString(), TotalGB: 12345, Comment: "canonical", Policy: &model.ClientPolicyOptions{Multiplier: "1.5"}}
	if err := database.GetDB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func passwordOwnerSettings(t *testing.T, protocol model.Protocol, accounts ...map[string]any) string {
	t.Helper()
	settings := map[string]any{"accounts": accounts}
	if protocol == model.Mixed {
		settings["auth"] = "password"
		settings["udp"] = true
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPasswordProxyOwnersPreserveCanonicalRecords(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			first, second := passwordOwner(t, "password-first@example.test"), passwordOwner(t, "password-second@example.test")
			usage := xray.ClientTraffic{Email: first.Email, Enable: true, Up: 123, Down: 456}
			if err := database.GetDB().Create(&usage).Error; err != nil {
				t.Fatal(err)
			}
			accounts := []map[string]any{
				{"user": "alice", "pass": "resource-A", "ownerClientId": first.StableID},
				{"user": "ALICE", "pass": "resource-alias", "ownerClientId": first.StableID},
				{"user": "用户", "pass": "resource-B", "ownerClientId": second.StableID},
			}
			svc := &InboundService{}
			ib, _, err := svc.AddInbound(&model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 24501, Settings: passwordOwnerSettings(t, protocol, accounts...)})
			if err != nil {
				t.Fatal(err)
			}
			if links := linksOf(t, ib.Id); len(links) != 2 || links[first.Id].ClientId != first.Id || links[second.Id].ClientId != second.Id {
				t.Fatalf("canonical memberships = %+v", links)
			}
			var saved map[string]any
			if err := json.Unmarshal([]byte(ib.Settings), &saved); err != nil {
				t.Fatal(err)
			}
			for i, raw := range saved["accounts"].([]any) {
				if got := raw.(map[string]any); !reflect.DeepEqual(got, accounts[i]) {
					t.Fatalf("resource account changed: %+v", got)
				}
			}
			for _, prior := range []model.ClientRecord{first, second} {
				var got model.ClientRecord
				if err := database.GetDB().First(&got, prior.Id).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, prior) {
					t.Fatalf("ownership overwrote shared record: %+v", got)
				}
			}
			if err := database.GetDB().First(&usage, usage.Id).Error; err != nil || usage.Up != 123 || usage.Down != 456 {
				t.Fatalf("ownership changed historical traffic: %+v %v", usage, err)
			}
			accounts[0]["pass"] = "rotated-resource"
			accounts[1]["ownerClientId"] = second.StableID
			edited := *ib
			edited.Settings = passwordOwnerSettings(t, protocol, accounts...)
			if _, _, err := svc.UpdateInbound(&edited); err != nil {
				t.Fatal(err)
			}
			for _, prior := range []model.ClientRecord{first, second} {
				var got model.ClientRecord
				if err := database.GetDB().First(&got, prior.Id).Error; err != nil || !reflect.DeepEqual(got, prior) {
					t.Fatalf("rotation/reassignment changed shared owner: %+v %v", got, err)
				}
			}
			edited.Settings = passwordOwnerSettings(t, protocol)
			if _, _, err := svc.UpdateInbound(&edited); err != nil {
				t.Fatal(err)
			}
			if len(linksOf(t, ib.Id)) != 0 {
				t.Fatal("last account removal kept membership")
			}
			var final model.Inbound
			if err := database.GetDB().First(&final, ib.Id).Error; err != nil {
				t.Fatal(err)
			}
			var settings map[string]any
			if err := json.Unmarshal([]byte(final.Settings), &settings); err != nil {
				t.Fatal(err)
			}
			if protocol == model.HTTP && settings["requireAuthentication"] != true {
				t.Fatal("last removal reopened anonymous HTTP")
			}
			if err := database.GetDB().First(&usage, usage.Id).Error; err != nil || usage.Up != 123 || usage.Down != 456 || usage.InboundId != 0 {
				t.Fatalf("detached history = %+v %v", usage, err)
			}
		})
	}
}

func TestPasswordProxyOwnerCommandsRejectBeforeWriting(t *testing.T) {
	for _, bad := range []string{"missing", "noauth", "remote", "mixed", "duplicate", "clients", "stats", "invalid-owner"} {
		t.Run(bad, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "password-owner@example.test")
			settings := map[string]any{"auth": "password", "accounts": []map[string]any{{"user": "alice", "pass": "resource", "ownerClientId": owner.StableID}}}
			ib := &model.Inbound{Protocol: model.Mixed, Listen: "127.0.0.1", Port: 24502}
			switch bad {
			case "missing":
				settings["accounts"].([]map[string]any)[0]["ownerClientId"] = uuid.NewString()
			case "invalid-owner":
				settings["accounts"].([]map[string]any)[0]["ownerClientId"] = "not-an-id"
			case "noauth":
				settings["auth"] = "noauth"
			case "remote":
				node := 1
				ib.NodeID = &node
			case "mixed":
				settings["accounts"] = append(settings["accounts"].([]map[string]any), map[string]any{"user": "unowned", "pass": "unowned"})
			case "duplicate":
				settings["accounts"] = append(settings["accounts"].([]map[string]any), map[string]any{"user": "alice", "pass": "second", "ownerClientId": owner.StableID})
			case "clients":
				settings["clients"] = []model.Client{*owner.ToClient()}
			case "stats":
				ib.ClientStats = []xray.ClientTraffic{{Email: owner.Email, Up: 999}}
			}
			data, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			ib.Settings = string(data)
			if _, _, err := (&InboundService{}).AddInbound(ib); err == nil {
				t.Fatal("untrusted ownership accepted")
			}
			var count int64
			if err := database.GetDB().Model(&model.Inbound{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("rejected owner persisted %d listeners: %v", count, err)
			}
			var got model.ClientRecord
			if err := database.GetDB().First(&got, owner.Id).Error; err != nil || !reflect.DeepEqual(got, owner) {
				t.Fatalf("rejected owner changed canonical record: %+v %v", got, err)
			}
		})
	}
}

func TestPasswordProxyOwnerLateSQLRollback(t *testing.T) {
	setupPolicyLedgerDB(t)
	first, second := passwordOwner(t, "rollback-first@example.test"), passwordOwner(t, "rollback-second@example.test")
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24503, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "original", "ownerClientId": first.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected membership write failure")
	name := "test-password-owner-late-failure"
	if err := database.GetDB().Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_inbounds" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.GetDB().Callback().Create().Remove(name)
	edited := *ib
	edited.Settings = passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "bob", "pass": "replacement", "ownerClientId": second.StableID})
	if _, _, err := svc.UpdateInbound(&edited); !errors.Is(err, injected) {
		t.Fatalf("late failure = %v", err)
	}
	var saved model.Inbound
	if err := database.GetDB().First(&saved, ib.Id).Error; err != nil || saved.Settings != ib.Settings {
		t.Fatalf("late error committed settings: %+v %v", saved, err)
	}
	if links := linksOf(t, ib.Id); len(links) != 1 || links[first.Id].ClientId != first.Id {
		t.Fatalf("late failure changed links: %+v", links)
	}
	var count int64
	if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", second.Email).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("late error created stat: %d %v", count, err)
	}
}

func TestPasswordProxyOwnerReadsRejectStaleMembership(t *testing.T) {
	for _, operation := range []string{"detail", "raw", "list", "all", "update"} {
		t.Run(operation, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "stale-owner@example.test")
			svc := &InboundService{}
			ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24504, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Where("inbound_id = ?", ib.Id).Delete(&model.ClientInbound{}).Error; err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "detail":
				_, err = svc.GetInboundDetail(ib.Id)
			case "raw":
				_, err = svc.GetInbound(ib.Id)
			case "list":
				_, err = svc.GetInbounds(ib.UserId)
			case "all":
				_, err = svc.GetAllInbounds()
			case "update":
				edited := *ib
				_, _, err = svc.UpdateInbound(&edited)
			}
			if err == nil {
				t.Fatal("stale account ownership accepted")
			}
		})
	}
}

func TestPasswordProxyOwnerCannotGainRemoteMembership(t *testing.T) {
	setupPolicyLedgerDB(t)
	owner := passwordOwner(t, "local-only-owner@example.test")
	owner.Policy = nil
	if err := database.GetDB().Exec("UPDATE clients SET policy_upload_bytes_per_second = NULL, policy_download_bytes_per_second = NULL, policy_multiplier = NULL WHERE id = ?", owner.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().First(&owner, owner.Id).Error; err != nil || owner.Policy != nil || owner.DesiredPolicyVersion != 0 {
		t.Fatalf("fixture did not remove explicit policy: %+v %v", owner, err)
	}
	ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.Mixed, Port: 24505, Settings: passwordOwnerSettings(t, model.Mixed, map[string]any{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	remote := mkInbound(t, 24506, model.VLESS, clientsSettings(t, nil))
	node := model.Node{Name: "password-remote", Address: "https://node.invalid", ApiToken: "test-token", Enable: true}
	if err := database.GetDB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(remote).Update("node_id", node.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&ClientService{}).SyncInbound(nil, remote.Id, []model.Client{*owner.ToClient()}); !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("remote attachment without explicit policy = %v", err)
	}
	if len(linksOf(t, remote.Id)) != 0 || len(linksOf(t, ib.Id)) != 1 {
		t.Fatal("failed remote attachment changed memberships")
	}
}

func TestPasswordProxyOwnerCoreIdentityAndLegacyAccounts(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "trusted-account@example.test")
			accounts := []map[string]any{{"user": "alice", "pass": "resource", "ownerClientId": owner.StableID, "clientId": "forged-client", "client_id": "forged-alias", "email": "forged@example.test"}}
			svc := &InboundService{}
			ib, _, err := svc.AddInbound(&model.Inbound{Protocol: protocol, Port: 24507, Settings: passwordOwnerSettings(t, protocol, accounts...)})
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]any
			if err := json.Unmarshal([]byte(ib.Settings), &settings); err != nil {
				t.Fatal(err)
			}
			account := settings["accounts"].([]any)[0].(map[string]any)
			for _, key := range []string{"clientId", "client_id", "email"} {
				if _, exists := account[key]; exists {
					t.Fatalf("untrusted core field persisted: %s", key)
				}
			}
			if account["ownerClientId"] != owner.StableID {
				t.Fatal("lost explicit canonical selection")
			}
			for _, operation := range []string{"sync", "delta"} {
				client := *owner.ToClient()
				client.Password = "must-not-merge"
				client.Comment = "must-not-change"
				var err error
				if operation == "sync" {
					err = (&ClientService{}).SyncInbound(nil, ib.Id, []model.Client{client})
				} else {
					err = (&ClientService{}).ApplyInboundClientDelta(nil, ib.Id, []model.Client{client}, nil)
				}
				if !errors.Is(err, ErrPasswordProxyOwner) {
					t.Fatalf("generic %s bypass = %v", operation, err)
				}
			}
			var saved model.ClientRecord
			if err := database.GetDB().First(&saved, owner.Id).Error; err != nil || !reflect.DeepEqual(saved, owner) {
				t.Fatalf("generic sync changed owner: %+v %v", saved, err)
			}
			legacy := []map[string]any{{"user": "duplicate", "pass": "first"}, {"user": "duplicate", "pass": "last"}}
			legacyIB, _, err := svc.AddInbound(&model.Inbound{Protocol: protocol, Port: 24508, Settings: passwordOwnerSettings(t, protocol, legacy...)})
			if err != nil {
				t.Fatal(err)
			}
			if len(linksOf(t, legacyIB.Id)) != 0 {
				t.Fatal("legacy credentials gained inferred owners")
			}
		})
	}
}

func TestPasswordProxyOwnerRequiresManagedConfiguration(t *testing.T) {
	setupPolicyLedgerDB(t)
	owner := passwordOwner(t, "password-preflight@example.test")
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24509, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(ib).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&XrayService{}).GetXrayConfig(); !errors.Is(err, xray.ErrClientPolicyCapability) {
		t.Fatalf("unmanaged owned configuration = %v", err)
	}
}

func TestPasswordProxyOwnerReadsAcceptCanonicalMembership(t *testing.T) {
	setupPolicyLedgerDB(t)
	owner := passwordOwner(t, "valid-owned-read@example.test")
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24510, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetInboundDetail(ib.Id); err != nil {
		t.Fatalf("canonical detail rejected: %v", err)
	}
	if _, err := svc.GetInbounds(ib.UserId); err != nil {
		t.Fatalf("canonical list rejected: %v", err)
	}
	if _, err := svc.GetAllInbounds(); err != nil {
		t.Fatalf("canonical full list rejected: %v", err)
	}
}

func TestPasswordProxyOwnerProtocolChangePreservesHistory(t *testing.T) {
	setupPolicyLedgerDB(t)
	owner := passwordOwner(t, "changed-protocol@example.test")
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24511, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", owner.Email).Updates(map[string]any{"up": 123, "down": 456}).Error; err != nil {
		t.Fatal(err)
	}
	changed := *ib
	changed.Protocol = model.VLESS
	changed.Settings = `{"clients":[],"decryption":"none"}`
	if _, _, err := svc.UpdateInbound(&changed); err != nil {
		t.Fatal(err)
	}
	var traffic xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", owner.Email).First(&traffic).Error; err != nil || traffic.Up != 123 || traffic.Down != 456 || traffic.InboundId != 0 {
		t.Fatalf("protocol change left stale history: %+v %v", traffic, err)
	}
}

func TestPasswordProxyOwnerNativeAliasesAndCaseVariants(t *testing.T) {
	for _, arrayKey := range []string{"users", "Users", "USERS", "uſers", "Accounts"} {
		t.Run(arrayKey, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "alias-owner@example.test")
			settings := map[string]any{arrayKey: []map[string]any{{"User": "alice", "Pass": "secret", "OwnerClientId": owner.StableID, "ClientId": "forged", "EMAIL": "forged@example.test"}}}
			if arrayKey == "users" {
				settings["accounts"] = nil
			}
			raw, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			svc := &InboundService{}
			ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24512, Settings: string(raw)})
			if err != nil {
				t.Fatal(err)
			}
			if links := linksOf(t, ib.Id); len(links) != 1 || links[owner.Id].ClientId != owner.Id {
				t.Fatalf("alias bypassed canonical membership: %+v", links)
			}
			var saved map[string]any
			if err := json.Unmarshal([]byte(ib.Settings), &saved); err != nil {
				t.Fatal(err)
			}
			accounts, ok := saved["accounts"].([]any)
			if !ok || len(accounts) != 1 {
				t.Fatalf("active alias not normalized: %+v", saved)
			}
			account := accounts[0].(map[string]any)
			if !reflect.DeepEqual(account, map[string]any{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID}) {
				t.Fatalf("noncanonical or forged metadata survived: %+v", account)
			}
			if saved["requireAuthentication"] != true {
				t.Fatal("alias HTTP not protected")
			}
		})
	}
	for _, arrayKey := range []string{"users", "Users", "Accounts"} {
		t.Run("noauth-"+arrayKey, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "alias-noauth@example.test")
			raw, err := json.Marshal(map[string]any{"auth": "noauth", arrayKey: []map[string]any{{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.Mixed, Port: 24513, Settings: string(raw)}); err == nil {
				t.Fatal("alias ownership accepted for noauth")
			}
		})
	}
}

func TestPasswordProxyOwnerMixedToEmptyHTTPRemainsProtected(t *testing.T) {
	setupPolicyLedgerDB(t)
	owner := passwordOwner(t, "conversion-owner@example.test")
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.Mixed, Port: 24514, Settings: passwordOwnerSettings(t, model.Mixed, map[string]any{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	edited := *ib
	edited.Protocol = model.HTTP
	edited.Settings = `{"accounts":[]}`
	if _, _, err := svc.UpdateInbound(&edited); err != nil {
		t.Fatal(err)
	}
	saved, err := svc.GetInbound(ib.Id)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(saved.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["requireAuthentication"] != true {
		t.Fatal("Mixed conversion reopened anonymous HTTP")
	}
	if len(linksOf(t, ib.Id)) != 0 {
		t.Fatal("empty conversion retained old membership")
	}
}

func TestPasswordProxyOwnerLegacyIgnoredEmailRemainsReadable(t *testing.T) {
	setupPolicyLedgerDB(t)
	ib := mkInbound(t, 24515, model.HTTP, `{"accounts":[{"user":"legacy","pass":"secret","email":"ignored-label"}]}`)
	svc := &InboundService{}
	if _, err := svc.GetInbound(ib.Id); err != nil {
		t.Fatalf("ignored legacy email prevents repair: %v", err)
	}
	if _, err := svc.GetInboundDetail(ib.Id); err != nil {
		t.Fatalf("ignored legacy email prevents detail: %v", err)
	}
	edited := *ib
	if _, _, err := svc.UpdateInbound(&edited); err != nil {
		t.Fatalf("cannot sanitize legacy metadata: %v", err)
	}
	saved, err := svc.GetInbound(ib.Id)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(saved.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	account := settings["accounts"].([]any)[0].(map[string]any)
	if _, exists := account["email"]; exists {
		t.Fatal("legacy ignored metadata not stripped on next write")
	}
	if account["user"] != "legacy" || account["pass"] != "secret" {
		t.Fatal("sanitization changed legacy credentials")
	}
}

func TestPasswordProxyOwnerPostgresLocksCompetingRemoteAttachment(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	if db.Name() != "postgres" {
		t.Skip("PostgreSQL row-lock regression; exercised by the explicit PostgreSQL gate")
	}
	owner := passwordOwner(t, "locked-owner@example.test")
	if err := db.Exec("UPDATE clients SET policy_upload_bytes_per_second = NULL, policy_download_bytes_per_second = NULL, policy_multiplier = NULL WHERE id = ?", owner.Id).Error; err != nil {
		t.Fatal(err)
	}
	owner.Policy = nil
	if err := db.First(&owner, owner.Id).Error; err != nil || owner.Policy != nil {
		t.Fatalf("unexpected explicit policy: %+v %v", owner, err)
	}
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24516, Settings: `{"accounts":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	remote := mkInbound(t, 24517, model.VLESS, clientsSettings(t, nil))
	node := model.Node{Name: "competing-remote", Address: "https://node.invalid", ApiToken: "test-token", Enable: true}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(remote).Update("node_id", node.Id).Error; err != nil {
		t.Fatal(err)
	}
	edited := *ib
	edited.Settings = passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID})
	if err := preparePasswordProxyOwnerCommand(&edited); err != nil {
		t.Fatal(err)
	}
	ready, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	localResult := make(chan error, 1)
	go func() {
		localResult <- db.Transaction(func(tx *gorm.DB) error {
			owners, err := resolvePasswordProxyOwners(tx, &edited)
			if err != nil {
				return err
			}
			close(ready)
			<-release
			if err := tx.Save(&edited).Error; err != nil {
				return err
			}
			return (&ClientService{}).syncPasswordProxyOwnerLinks(tx, &edited, owners)
		})
	}()
	select {
	case <-ready:
	case err := <-localResult:
		t.Fatalf("failed before lock barrier: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("owner resolution did not acquire its lock")
	}
	remoteResult := make(chan error, 1)
	go func() {
		remoteResult <- db.Transaction(func(tx *gorm.DB) error {
			return (&ClientService{}).SyncInbound(tx, remote.Id, []model.Client{*owner.ToClient()})
		})
	}()
	select {
	case err := <-remoteResult:
		t.Fatalf("remote edit bypassed held client lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-localResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("local ownership transaction did not finish")
	}
	select {
	case err := <-remoteResult:
		if !errors.Is(err, ErrClientPolicyLedger) {
			t.Fatalf("competing remote attachment = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("remote guard did not finish after release")
	}
	if len(linksOf(t, ib.Id)) != 1 || len(linksOf(t, remote.Id)) != 0 {
		t.Fatal("competing edit committed a remote membership")
	}
}

func TestPasswordProxyOwnerDormantAliasAndAmbiguousCase(t *testing.T) {
	setupPolicyLedgerDB(t)
	owner := passwordOwner(t, "dormant-owner@example.test")
	dormant, _ := json.Marshal(map[string]any{"accounts": []any{}, "users": []map[string]any{{"user": "alice", "pass": "secret", "ownerClientId": owner.StableID}}})
	if _, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24518, Settings: string(dormant)}); err == nil {
		t.Fatal("dormant users silently acquired ownership")
	}
	for _, raw := range []string{
		`{"accounts":[],"ACCOUNTS":[]}`,
		`{"accounts":[{"user":"alice","User":"bob","pass":"secret"}]}`,
	} {
		if _, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24519, Settings: raw}); err == nil {
			t.Fatal("ambiguous native field spelling accepted")
		}
	}
	legacy, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24520, Settings: `{"accounts":[],"users":[{"user":"unused","pass":"retained"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(legacy.Settings), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields["accounts"].([]any)) != 0 || fields["users"].([]any)[0].(map[string]any)["pass"] != "retained" {
		t.Fatal("legacy empty-accounts precedence or dormant credentials changed")
	}
}

func TestPasswordProxyOwnerListenerDeletionPreservesSiblingHistory(t *testing.T) {
	setupPolicyLedgerDB(t)
	owner := passwordOwner(t, "deleted-listener-owner@example.test")
	svc := &InboundService{}
	create := func(port int) *model.Inbound {
		ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: port, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": owner.StableID})})
		if err != nil {
			t.Fatal(err)
		}
		return ib
	}
	first, second := create(24521), create(24522)
	if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", owner.Email).Updates(map[string]any{"up": 123, "down": 456}).Error; err != nil {
		t.Fatal(err)
	}
	for i, ib := range []*model.Inbound{first, second} {
		if _, err := svc.DelInbound(ib.Id); err != nil {
			t.Fatal(err)
		}
		expected := 0
		if i == 0 {
			expected = second.Id
		}
		var traffic xray.ClientTraffic
		if err := database.GetDB().Where("email = ?", owner.Email).First(&traffic).Error; err != nil || traffic.Up != 123 || traffic.Down != 456 || traffic.InboundId != expected {
			t.Fatalf("deleted listener history = %+v %v", traffic, err)
		}
	}
	var saved model.ClientRecord
	if err := database.GetDB().First(&saved, owner.Id).Error; err != nil || !reflect.DeepEqual(saved, owner) {
		t.Fatalf("resource deletion changed reusable owner: %+v %v", saved, err)
	}
}

func TestPasswordProxyOwnerLegacyConversionDoesNotRequireCustomCore(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.Mixed, Port: 24523, Settings: `{"auth":"password","accounts":[{"user":"legacy","pass":"secret"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	edited := *ib
	edited.Protocol = model.HTTP
	edited.Settings = `{"accounts":[]}`
	if _, _, err := svc.UpdateInbound(&edited); err != nil {
		t.Fatal(err)
	}
	saved, err := svc.GetInbound(ib.Id)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(saved.Settings), &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["requireAuthentication"]; exists {
		t.Fatal("legacy protocol conversion gained a custom-core requirement")
	}
}
