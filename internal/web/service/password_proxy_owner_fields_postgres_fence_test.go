package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerFieldsPostgresFinalSelectionFences(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires dedicated PostgreSQL row-level interleaving")
	}
	for _, boundary := range []string{"legacy-single", "legacy-bulk", "bulk-final-metadata"} {
		t.Run(boundary, func(t *testing.T) {
			owner, password, ordinary := passwordUpdateSibling(t)
			if err := database.GetDB().Model(password).Update("settings", `{"accounts":[],"requireAuthentication":true}`).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Where("client_id = ? AND inbound_id = ?", owner.Id, password.Id).Delete(&model.ClientInbound{}).Error; err != nil {
				t.Fatal(err)
			}
			replacement := passwordOwner(t, "review-final-replacement")
			if err := database.GetDB().Create(&xray.ClientTraffic{Email: replacement.Email, Enable: true, Up: 81, Down: 92}).Error; err != nil {
				t.Fatal(err)
			}
			previous := panelruntime.GetManager()
			t.Cleanup(func() { panelruntime.SetManager(previous) })
			var legacyCommitted, changed atomic.Bool
			panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) { legacyCommitted.Store(true); return true, nil }}))
			const callback = "review-correction-final-identity-fence"
			if err := database.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
				if q.Statement.Table != "clients" || q.Statement.Clauses["FOR"].Expression == nil || !isSerializedTx(q) {
					return
				}
				if boundary == "bulk-final-metadata" && !legacyCommitted.Load() {
					return
				}
				if !changed.CompareAndSwap(false, true) {
					return
				}
				q.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Update("email", "review-final-original-renamed").Error; err != nil {
						return err
					}
					if err := tx.Model(&model.ClientRecord{}).Where("id = ?", replacement.Id).Update("email", owner.Email).Error; err != nil {
						return err
					}
					if err := tx.Model(&xray.ClientTraffic{}).Where("email = ?", owner.Email).Update("email", "review-final-original-renamed").Error; err != nil {
						return err
					}
					if err := tx.Model(&xray.ClientTraffic{}).Where("email = ?", replacement.Email).Update("email", owner.Email).Error; err != nil {
						return err
					}
					var current model.Inbound
					if err := tx.First(&current, ordinary.Id).Error; err != nil {
						return err
					}
					var settings map[string]any
					if err := json.Unmarshal([]byte(current.Settings), &settings); err != nil {
						return err
					}
					entries := settings["clients"].([]any)
					entries[0].(map[string]any)["email"] = "review-final-original-renamed"
					wire := *replacement.ToClient()
					wire.Email = owner.Email
					settings["clients"] = append(entries, wire)
					encoded, err := json.Marshal(settings)
					if err != nil {
						return err
					}
					if err := tx.Model(&model.Inbound{}).Where("id = ?", ordinary.Id).Update("settings", string(encoded)).Error; err != nil {
						return err
					}
					return tx.Create(&model.ClientInbound{ClientId: replacement.Id, InboundId: ordinary.Id}).Error
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			var err error
			if boundary == "legacy-single" {
				_, err = (&ClientService{}).ResetClientIpLimitByEmail(&InboundService{}, owner.Email, 3)
			} else {
				var result BulkSetEnableResult
				result, _, err = (&ClientService{}).BulkSetEnable(&InboundService{}, []string{owner.Email}, false)
				if err == nil && len(result.Skipped) > 0 {
					err = errors.New(result.Skipped[0].Reason)
				}
			}
			if !changed.Load() || err == nil || !strings.Contains(err.Error(), ErrManagedConfigStale.Error()) {
				t.Fatalf("final fence boundary=%s changed=%v err=%v", boundary, changed.Load(), err)
			}
			current, readErr := (&ClientService{}).GetByID(replacement.Id)
			if readErr != nil || !current.Enable || current.LimitIP != replacement.LimitIP {
				t.Fatalf("replacement mutated: %+v/%v", current, readErr)
			}
			traffic := trafficOf(t, owner.Email)
			if !traffic.Enable || traffic.Up != 81 || traffic.Down != 92 {
				t.Fatalf("replacement traffic mutated: %+v", traffic)
			}
		})
	}
}
