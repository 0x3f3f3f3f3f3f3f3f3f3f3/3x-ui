package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerFieldsRelativeExpiryFirstUseSibling(t *testing.T) {
	owner, _, _ := passwordUpdateSibling(t)
	if _, err := (&ClientService{}).ResetClientExpiryTimeByEmail(&InboundService{}, owner.Email, -86400000); err != nil {
		t.Fatal(err)
	}
	if err := BindClientPolicySource("local", "review-relative-expiry", 1); err != nil {
		t.Fatal(err)
	}
	seed, err := PrepareClientPolicyLedger("review-relative-expiry", owner.StableID)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := PrepareClientPolicies([]string{owner.StableID})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "engine.db")
	if err := clientpolicy.CreateStore(path, "review-relative-expiry"); err != nil {
		t.Fatal(err)
	}
	engine, err := clientpolicy.OpenPersistentEngine(path, "review-relative-expiry")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := engine.Initialize(policies[0], clientpolicy.Usage{RawUpload: seed.RawUpload, RawDownload: seed.RawDownload, BilledBytes: seed.BilledBytes}); err != nil {
		t.Fatal(err)
	}
	session, err := engine.Open(context.Background(), clientpolicy.Metadata{ClientID: owner.StableID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Admit(clientpolicy.Upload, 3); err != nil {
		t.Fatal(err)
	}
	if err := engine.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	events, err := engine.ReadLedger(0, 1000)
	if err != nil || len(events) != 1 || events[0].FirstUsedAt <= 0 {
		t.Fatalf("actual receipt=%+v/%v", events, err)
	}
	event := events[0]
	page := &command.LedgerPage{NextSequence: event.Sequence, Records: []*command.LedgerRecord{{InstanceId: event.InstanceID, Epoch: event.Epoch, Sequence: event.Sequence, ClientId: event.ClientID, PolicyVersion: event.PolicyVersion, FirstUsedAt: event.FirstUsedAt, Usage: &command.Usage{RawUpload: event.Usage.RawUpload, RawDownload: event.Usage.RawDownload, BilledBytes: event.Usage.BilledBytes, Remainder: event.Usage.Remainder}}}}
	if err := SettleClientPolicyLedger("review-relative-expiry", 1, 0, page); err != nil {
		t.Fatalf("public relative expiry cannot settle actual first-use receipt: %v", err)
	}
}

func TestPasswordProxyOwnerFieldsClassificationLossAndEmailReuse(t *testing.T) {
	for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			owner, password, ordinary := passwordUpdateSibling(t)
			replacement := passwordOwner(t, "review-name-replacement")
			if err := database.GetDB().Create(&xray.ClientTraffic{Email: replacement.Email, Enable: true}).Error; err != nil {
				t.Fatal(err)
			}
			var changed atomic.Bool
			const callback = "review-lose-password-reuse-label"
			if err := database.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
				if q.Statement.Table != "client_inbounds" || !strings.Contains(fmt.Sprint(q.Statement.Clauses["WHERE"].Expression), "protocol IN") || !changed.CompareAndSwap(false, true) {
					return
				}
				q.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(password).Update("settings", `{"accounts":[],"requireAuthentication":true}`).Error; err != nil {
						return err
					}
					if err := tx.Where("client_id = ? AND inbound_id = ?", owner.Id, password.Id).Delete(&model.ClientInbound{}).Error; err != nil {
						return err
					}
					if err := tx.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Update("email", "review-owner-new-name").Error; err != nil {
						return err
					}
					if err := tx.Model(&model.ClientRecord{}).Where("id = ?", replacement.Id).Update("email", owner.Email).Error; err != nil {
						return err
					}
					if err := tx.Table("client_traffics").Where("email = ?", owner.Email).Update("email", "review-owner-new-name").Error; err != nil {
						return err
					}
					if err := tx.Table("client_traffics").Where("email = ?", replacement.Email).Update("email", owner.Email).Error; err != nil {
						return err
					}
					var settings map[string]any
					if err := json.Unmarshal([]byte(ordinary.Settings), &settings); err != nil {
						return err
					}
					entries := settings["clients"].([]any)
					entries[0].(map[string]any)["email"] = "review-owner-new-name"
					replacementWire := *replacement.ToClient()
					replacementWire.Email = owner.Email
					settings["clients"] = append(entries, replacementWire)
					encoded, err := json.Marshal(settings)
					if err != nil {
						return err
					}
					if err := tx.Model(ordinary).Update("settings", string(encoded)).Error; err != nil {
						return err
					}
					return tx.Create(&model.ClientInbound{ClientId: replacement.Id, InboundId: ordinary.Id}).Error
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			err := passwordOwnerFieldOperation(&ClientService{}, &InboundService{}, owner.Email, operation)
			current, readErr := (&ClientService{}).GetByID(replacement.Id)
			if !changed.Load() || readErr != nil {
				t.Fatalf("fixture changed=%v read=%v", changed.Load(), readErr)
			}
			t.Logf("operation=%s error=%v replacement enable=%v limit=%d quota=%d expiry=%d", operation, err, current.Enable, current.LimitIP, current.TotalGB, current.ExpiryTime)
			if err == nil || !strings.Contains(fmt.Sprint(err), ErrManagedConfigStale.Error()) {
				t.Errorf("name-reuse classification drift accepted: %v", err)
			}
			if !current.Enable || current.LimitIP != replacement.LimitIP || current.TotalGB != replacement.TotalGB || current.ExpiryTime != replacement.ExpiryTime {
				t.Error("operation changed old-name replacement")
			}
		})
	}
}

func TestPasswordProxyOwnerFieldsClassificationGain(t *testing.T) {
	for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "review-gain-owner")
			ordinary := mkInbound(t, 24902, model.VLESS, `{"decryption":"none","clients":[]}`)
			if _, err := (&ClientService{}).Attach(&InboundService{}, owner.Id, []int{ordinary.Id}); err != nil {
				t.Fatal(err)
			}
			var changed atomic.Bool
			const callback = "review-gain-password"
			if err := database.GetDB().Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) {
				if q.Statement.Table != "client_inbounds" || !strings.Contains(fmt.Sprint(q.Statement.Clauses["WHERE"].Expression), "protocol IN") || !changed.CompareAndSwap(false, true) {
					return
				}
				q.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					password := model.Inbound{Protocol: model.HTTP, Port: 24903, Listen: "127.0.0.1", Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": owner.StableID})}
					if err := preparePasswordProxyOwnerCommand(&password); err != nil {
						return err
					}
					if err := tx.Create(&password).Error; err != nil {
						return err
					}
					if err := tx.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: password.Id}).Error; err != nil {
						return err
					}
					return tx.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Update("password", "current-shared-secret").Error
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			err := passwordOwnerFieldOperation(&ClientService{}, &InboundService{}, owner.Email, operation)
			current, readErr := (&ClientService{}).GetByID(owner.Id)
			if !changed.Load() || readErr != nil {
				t.Fatalf("fixture changed=%v read=%v", changed.Load(), readErr)
			}
			t.Logf("operation=%s error=%v canonicalPassword=%s", operation, err, current.Password)
			if current.Password != "current-shared-secret" {
				t.Error("late owner classification replayed wire credentials into canonical password")
			}
		})
	}
}

func TestPasswordProxyOwnerFieldsClassificationLoss(t *testing.T) {
	for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			owner, password, _ := passwordUpdateSibling(t)
			var changed atomic.Bool
			const callback = "review-lose-last-password"
			if err := database.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
				if q.Statement.Table != "client_inbounds" || !strings.Contains(fmt.Sprint(q.Statement.Clauses["WHERE"].Expression), "protocol IN") || !changed.CompareAndSwap(false, true) {
					return
				}
				q.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(password).Update("settings", `{"accounts":[],"requireAuthentication":true}`).Error; err != nil {
						return err
					}
					if err := tx.Where("client_id = ? AND inbound_id = ?", owner.Id, password.Id).Delete(&model.ClientInbound{}).Error; err != nil {
						return err
					}
					return tx.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Updates(map[string]any{"password": "concurrent-current-secret", "limit_ip": 7, "comment": "concurrent-current-comment"}).Error
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			err := passwordOwnerFieldOperation(&ClientService{}, &InboundService{}, owner.Email, operation)
			current, readErr := (&ClientService{}).GetByID(owner.Id)
			if !changed.Load() || readErr != nil {
				t.Fatalf("fixture changed=%v read=%v", changed.Load(), readErr)
			}
			t.Logf("operation=%s error=%v current password=%s limit=%d comment=%s", operation, err, current.Password, current.LimitIP, current.Comment)
			if err == nil || !strings.Contains(fmt.Sprint(err), ErrManagedConfigStale.Error()) {
				t.Errorf("classification drift accepted: %v", err)
			}
			if current.Password != "concurrent-current-secret" || current.Comment != "concurrent-current-comment" || current.LimitIP != 7 {
				t.Error("classification fallback replayed stale unrelated fields")
			}
		})
	}
}

func TestPasswordProxyOwnerFieldsBulkRetainsPriorSuccess(t *testing.T) {
	owner, _, _ := passwordUpdateSibling(t)
	legacy := passwordOwner(t, "review-independent-legacy")
	ordinary := mkInbound(t, 24901, model.VLESS, `{"decryption":"none","clients":[]}`)
	if _, err := (&ClientService{}).Attach(&InboundService{}, legacy.Id, []int{ordinary.Id}); err != nil {
		t.Fatal(err)
	}
	previous := panelruntime.GetManager()
	t.Cleanup(func() { panelruntime.SetManager(previous) })
	var applied atomic.Bool
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) { applied.Store(true); return false, nil }}))
	injected := errors.New("injected later legacy membership read failure")
	const callback = "review-fail-legacy-read"
	if err := database.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
		if applied.Load() && q.Statement.Table == "client_inbounds" {
			q.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
	result, restart, err := (&ClientService{}).BulkSetEnable(&InboundService{}, []string{owner.Email, legacy.Email}, false)
	current, readErr := (&ClientService{}).GetByID(owner.Id)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !errors.Is(err, injected) || !applied.Load() || current.Enable {
		t.Fatalf("fixture: result=%+v restart=%v error=%v applied=%v ownerEnable=%v", result, restart, err, applied.Load(), current.Enable)
	}
	if !restart || result.Changed != 1 {
		t.Fatalf("lost committed owned result: result=%+v restart=%v err=%v", result, restart, err)
	}
}
