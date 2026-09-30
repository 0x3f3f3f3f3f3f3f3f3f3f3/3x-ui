package service

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerSingleUpdateScopeBeforeFilter(t *testing.T) {
	for _, drift := range []string{"remote", "malformed-missing-link"} {
		t.Run(drift, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "update-scope-owner")
			svc, clients := &InboundService{}, &ClientService{}
			primary, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24555, Settings: passwordOwnerSettings(t, model.HTTP,
				map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": owner.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			ordinary := mkInbound(t, 24556, model.VLESS, `{"decryption":"none","clients":[]}`)
			if _, err := clients.Attach(svc, owner.Id, []int{ordinary.Id}); err != nil {
				t.Fatal(err)
			}
			ordinary, err = svc.GetInbound(ordinary.Id)
			if err != nil {
				t.Fatal(err)
			}
			if drift == "remote" {
				remote := mkInbound(t, 24557, model.VLESS, `{"decryption":"none","clients":[]}`)
				if err := database.GetDB().Model(remote).Update("node_id", 99).Error; err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Create(&model.ClientInbound{ClientId: owner.Id, InboundId: remote.Id}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				primary.Settings = fmt.Sprintf(`{"accounts":[{"user":"alice","pass":17,"ownerClientId":"%s"}],"requireAuthentication":true}`, owner.StableID)
				if err := database.GetDB().Model(primary).Update("settings", primary.Settings).Error; err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Where("client_id = ? AND inbound_id = ?", owner.Id, primary.Id).Delete(&model.ClientInbound{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			before, err := clients.GetByID(owner.Id)
			if err != nil {
				t.Fatal(err)
			}
			updated := *before.ToClient()
			updated.Comment = "must-not-save"
			if _, err := clients.Update(svc, owner.Id, updated, 0, ordinary.Id); err == nil {
				t.Fatal("inbound filter hid invalid password owner scope")
			}
			for _, ib := range []*model.Inbound{primary, ordinary} {
				var saved model.Inbound
				if err := database.GetDB().First(&saved, ib.Id).Error; err != nil || saved.Settings != ib.Settings {
					t.Fatalf("invalid scope changed resource before fanout: %+v %v", saved, err)
				}
			}
			if after, err := clients.GetByID(owner.Id); err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid scope changed canonical record: %+v %v", after, err)
			}
		})
	}
}

func TestPasswordProxyOwnerSingleUpdateSQLRollback(t *testing.T) {
	setupPolicyLedgerDB(t)
	owner := passwordOwner(t, "update-rollback-owner")
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: owner.Email, Enable: true, Up: 123, Down: 456}).Error; err != nil {
		t.Fatal(err)
	}
	svc, clients := &InboundService{}, &ClientService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24558, Settings: passwordOwnerSettings(t, model.HTTP,
		map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	before := trafficOf(t, owner.Email)
	injected := errors.New("injected canonical owner update failure")
	const callback = "test-password-update-sql-failure"
	if err := database.GetDB().Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "clients" && strings.Contains(fmt.Sprint(tx.Statement.Dest), "comment") {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.GetDB().Callback().Update().Remove(callback) })
	updated := *owner.ToClient()
	updated.Email, updated.Comment, updated.TotalGB = "update-rollback-renamed", "must-rollback", 999
	if _, err := clients.Update(svc, owner.Id, updated, 0); !errors.Is(err, injected) {
		t.Fatalf("late canonical error was hidden: %v", err)
	}
	if current, err := clients.GetByID(owner.Id); err != nil || !reflect.DeepEqual(current, &owner) || !reflect.DeepEqual(trafficOf(t, owner.Email), before) {
		t.Fatalf("failed canonical write committed record/history: %+v %v", current, err)
	}
	var saved model.Inbound
	if err := database.GetDB().First(&saved, ib.Id).Error; err != nil || saved.Settings != ib.Settings {
		t.Fatalf("failed shared update changed aliases: %+v %v", saved, err)
	}
}

func TestPasswordProxyOwnerSingleUpdateRejectsReusedEmail(t *testing.T) {
	for _, sibling := range []bool{false, true} {
		t.Run(map[bool]string{false: "password-only", true: "ordinary-sibling"}[sibling], func(t *testing.T) {
			setupPolicyLedgerDB(t)
			first, second := passwordOwner(t, "update-identity-first"), passwordOwner(t, "update-identity-second")
			if err := database.GetDB().Create(&xray.ClientTraffic{Email: second.Email, Enable: true, Up: 33, Down: 44}).Error; err != nil {
				t.Fatal(err)
			}
			svc, clients := &InboundService{}, &ClientService{}
			_, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24559, Settings: passwordOwnerSettings(t, model.HTTP,
				map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": first.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			if sibling {
				ordinary := mkInbound(t, 24560, model.VLESS, `{"decryption":"none","clients":[]}`)
				if _, err := clients.Attach(svc, first.Id, []int{ordinary.Id}); err != nil {
					t.Fatal(err)
				}
			}
			var changed atomic.Bool
			const callback = "test-password-update-reused-email"
			if err := database.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table != "inbounds" || !changed.CompareAndSwap(false, true) {
					return
				}
				query.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					for _, rename := range []struct {
						id        int
						old, next string
					}{{first.Id, first.Email, "update-concurrent-rename"}, {second.Id, second.Email, first.Email}} {
						if err := tx.Model(&model.ClientRecord{}).Where("id = ?", rename.id).Update("email", rename.next).Error; err != nil {
							return err
						}
						if err := tx.Table("client_traffics").Where("email = ?", rename.old).Update("email", rename.next).Error; err != nil {
							return err
						}
					}
					return nil
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			updated := *first.ToClient()
			updated.Email, updated.Comment = "update-requested-rename", "must-not-save"
			if _, err := clients.Update(svc, first.Id, updated, 0); !errors.Is(err, ErrManagedConfigStale) {
				t.Fatalf("reused email did not fence canonical update: %v", err)
			}
			if replacement := trafficOf(t, first.Email); replacement.Up != 33 || replacement.Down != 44 {
				t.Fatalf("update changed replacement history: %+v", replacement)
			}
			if replacement, err := clients.GetByID(second.Id); err != nil || replacement.Comment != second.Comment || replacement.Email != first.Email {
				t.Fatalf("update changed replacement identity: %+v %v", replacement, err)
			}
		})
	}
}
