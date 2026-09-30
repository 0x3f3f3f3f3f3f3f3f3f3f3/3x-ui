package service

import (
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerRemovalPinsSingleIdentityAcrossRename(t *testing.T) {
	for _, action := range []string{"detach", "delete"} {
		t.Run(action, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			first, second := passwordOwner(t, "removal-email-first"), passwordOwner(t, "removal-email-second")
			if err := database.GetDB().Create(&xray.ClientTraffic{Email: second.Email, Enable: true, Up: 33, Down: 44}).Error; err != nil {
				t.Fatal(err)
			}
			svc, clients := &InboundService{}, &ClientService{}
			ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24541, Settings: passwordOwnerSettings(t, model.HTTP,
				map[string]any{"user": "first", "pass": "first-secret", "ownerClientId": first.StableID},
				map[string]any{"user": "second", "pass": "second-secret", "ownerClientId": second.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			var inboundReads atomic.Int32
			const name = "test-password-removal-rename"
			if err := database.GetDB().Callback().Query().Before("gorm:query").Register(name, func(query *gorm.DB) {
				if query.Statement.Table != "inbounds" || inboundReads.Add(1) != 2 {
					return
				}
				query.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					for _, rename := range []struct {
						id        int
						old, next string
					}{{first.Id, first.Email, "removal-renamed-first"}, {second.Id, second.Email, first.Email}} {
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
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(name) })
			if action == "detach" {
				_, err = clients.Detach(svc, first.Id, []int{ib.Id})
			} else {
				_, err = clients.Delete(svc, first.Id, false)
			}
			var saved model.Inbound
			if dbErr := database.GetDB().First(&saved, ib.Id).Error; dbErr != nil {
				t.Fatal(dbErr)
			}
			_, accounts, parseErr := passwordProxyAccounts(&saved)
			if parseErr != nil || len(accounts) != 1 || accounts[0].OwnerClientID != second.StableID {
				t.Fatalf("single operation removed the wrong canonical owner after rename: accounts=%+v err=%v parse=%v", accounts, err, parseErr)
			}
			if action == "detach" && err != nil || action == "delete" && !errors.Is(err, ErrManagedConfigStale) {
				t.Fatalf("unexpected pinned identity result: %v", err)
			}
			if replacement := trafficOf(t, first.Email); replacement.Up != 33 || replacement.Down != 44 {
				t.Fatalf("old snapshot cleaned replacement owner's history: %+v", replacement)
			}
			if retained, err := clients.GetByID(first.Id); err != nil || retained.Email != "removal-renamed-first" {
				t.Fatalf("changed snapshot deleted the canonical identity: %+v %v", retained, err)
			}
			if action == "delete" {
				if _, err := clients.Delete(svc, first.Id, false); err != nil {
					t.Fatalf("current-identity retry failed: %v", err)
				}
				if replacement := trafficOf(t, first.Email); replacement.Up != 33 || replacement.Down != 44 {
					t.Fatalf("retry cleaned replacement owner's history: %+v", replacement)
				}
			}
		})
	}
}
