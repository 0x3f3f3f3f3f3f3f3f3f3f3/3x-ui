package service

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestPasswordProxyOwnerRemovalScopeBeforeFanout(t *testing.T) {
	for _, action := range []string{"detach", "bulk-detach", "delete", "bulk-delete"} {
		t.Run(action, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "removal-remote-owner")
			clients, inbounds := &ClientService{}, &InboundService{}
			primary, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24533, Settings: passwordOwnerSettings(t, model.HTTP,
				map[string]any{"user": "resource", "pass": "resource-secret", "ownerClientId": owner.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			local := mkInbound(t, 24534, model.VLESS, clientsSettings(t, []model.Client{*owner.ToClient()}))
			remote := mkInbound(t, 24535, model.VLESS, clientsSettings(t, []model.Client{*owner.ToClient()}))
			nodeID := 99
			if err := database.GetDB().Model(remote).Update("node_id", nodeID).Error; err != nil {
				t.Fatal(err)
			}
			// Deliberate saved-data drift: normal attach already rejects this scope.
			for _, id := range []int{local.Id, remote.Id} {
				if err := database.GetDB().Create(&model.ClientInbound{ClientId: owner.Id, InboundId: id}).Error; err != nil {
					t.Fatal(err)
				}
			}
			before := map[int]map[int]model.ClientInbound{}
			for _, id := range []int{primary.Id, local.Id, remote.Id} {
				before[id] = linksOf(t, id)
			}
			rejected := false
			switch action {
			case "detach":
				_, err = clients.Detach(inbounds, owner.Id, []int{local.Id})
				rejected = err != nil
			case "bulk-detach":
				var result *BulkDetachResult
				result, _, err = clients.BulkDetach(inbounds, []string{owner.Email}, []int{local.Id})
				rejected = err != nil || len(result.Errors) != 0
			case "delete":
				_, err = clients.Delete(inbounds, owner.Id, true)
				rejected = err != nil
			case "bulk-delete":
				var result BulkDeleteResult
				result, _, err = clients.BulkDelete(inbounds, []string{owner.Email}, true)
				rejected = err != nil || len(result.Skipped) != 0
			}
			if !rejected {
				t.Fatal("unsupported remote owner scope passed the public operation")
			}
			for _, ib := range []*model.Inbound{primary, local, remote} {
				var saved model.Inbound
				if err := database.GetDB().First(&saved, ib.Id).Error; err != nil || saved.Settings != ib.Settings || !reflect.DeepEqual(linksOf(t, ib.Id), before[ib.Id]) {
					t.Fatalf("rejected scope changed resource %d before fanout: settings=%s links=%+v err=%v", ib.Id, saved.Settings, linksOf(t, ib.Id), err)
				}
			}
			var saved model.ClientRecord
			if err := database.GetDB().First(&saved, owner.Id).Error; err != nil || !reflect.DeepEqual(saved, owner) {
				t.Fatalf("rejected scope changed canonical record: %+v %v", saved, err)
			}
		})
	}
}

func TestPasswordProxyOwnerRemovalSQLRollback(t *testing.T) {
	setupPolicyLedgerDB(t)
	first, second := passwordOwner(t, "removal-rollback-first"), passwordOwner(t, "removal-rollback-second")
	svc := &InboundService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24536, Settings: passwordOwnerSettings(t, model.HTTP,
		map[string]any{"user": "first", "pass": "first-secret", "ownerClientId": first.StableID},
		map[string]any{"user": "second", "pass": "second-secret", "ownerClientId": second.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	before := linksOf(t, ib.Id)
	traffic := trafficOf(t, first.Email)
	injected := errors.New("password removal membership failure")
	const name = "test-password-removal-membership-failure"
	if err := database.GetDB().Callback().Delete().Before("gorm:delete").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_inbounds" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.GetDB().Callback().Delete().Remove(name) })
	if _, err := (&ClientService{}).Detach(svc, first.Id, []int{ib.Id}); !errors.Is(err, injected) {
		t.Fatalf("membership failure was not returned: %v", err)
	}
	var saved model.Inbound
	if err := database.GetDB().First(&saved, ib.Id).Error; err != nil || saved.Settings != ib.Settings || !reflect.DeepEqual(linksOf(t, ib.Id), before) || !reflect.DeepEqual(trafficOf(t, first.Email), traffic) {
		t.Fatalf("failed SQL removal committed settings, membership or history: %+v %v", saved, err)
	}
}

func TestPasswordProxyOwnerRemovalCannotHideMissingMembership(t *testing.T) {
	for _, variant := range []struct{ name, settings string }{
		{"valid", ""},
		{"numeric-password", `{"accounts":[{"user":"resource","pass":"secret","ownerClientId":"%s"},{"user":"broken","pass":17}],"requireAuthentication":true}`},
		{"folded-fields", `{"Accounts":[{"User":"resource","Pass":17,"OwnerClientID":"%s"}],"requireAuthentication":true}`},
		{"conflicting-arrays", `{"accounts":[],"Accounts":[{"user":"resource","pass":"secret","ownerClientId":"%s"}],"requireAuthentication":true}`},
		{"dormant-users", `{"accounts":[],"users":[{"user":"resource","pass":"secret","ownerClientId":"%s"}],"requireAuthentication":true}`},
		{"object-instead-of-array", `{"accounts":{"user":"resource","pass":17,"ownerClientId":"%s"},"requireAuthentication":true}`},
	} {
		for _, action := range []string{"detach", "delete"} {
			t.Run(fmt.Sprintf("%s/%s", action, variant.name), func(t *testing.T) {
				setupPolicyLedgerDB(t)
				owner := passwordOwner(t, "removal-missing-link")
				svc, clients := &InboundService{}, &ClientService{}
				ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24540, Settings: passwordOwnerSettings(t, model.HTTP,
					map[string]any{"user": "resource", "pass": "secret", "ownerClientId": owner.StableID})})
				if err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Where("client_id = ? AND inbound_id = ?", owner.Id, ib.Id).Delete(&model.ClientInbound{}).Error; err != nil {
					t.Fatal(err)
				}
				if variant.settings != "" {
					ib.Settings = fmt.Sprintf(variant.settings, owner.StableID)
					if err := database.GetDB().Model(ib).Update("settings", ib.Settings).Error; err != nil {
						t.Fatal(err)
					}
				}
				if action == "detach" {
					_, err = clients.Detach(svc, owner.Id, []int{ib.Id})
				} else {
					_, err = clients.Delete(svc, owner.Id, true)
				}
				if !errors.Is(err, ErrPasswordProxyOwner) {
					t.Fatalf("missing membership hid an owned credential: %v", err)
				}
				var saved model.Inbound
				if err := database.GetDB().First(&saved, ib.Id).Error; err != nil || saved.Settings != ib.Settings {
					t.Fatalf("invalid owner graph changed settings: %+v %v", saved, err)
				}
				if _, err := clients.GetByID(owner.Id); err != nil {
					t.Fatalf("invalid owner graph deleted canonical client: %v", err)
				}
			})
		}
	}
}
