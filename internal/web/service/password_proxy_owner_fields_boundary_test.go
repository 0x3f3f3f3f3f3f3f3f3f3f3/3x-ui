package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func passwordOwnerFieldOperation(clients *ClientService, inbounds *InboundService, email, operation string) error {
	switch operation {
	case "enable":
		_, _, err := clients.SetClientEnableByEmail(inbounds, email, false)
		return err
	case "toggle":
		_, _, err := clients.ToggleClientEnableByEmail(inbounds, email)
		return err
	case "ip-limit":
		_, err := clients.ResetClientIpLimitByEmail(inbounds, email, 3)
		return err
	case "expiry":
		_, err := clients.ResetClientExpiryTimeByEmail(inbounds, email, 1900000000000)
		return err
	case "quota":
		_, err := clients.ResetClientTrafficLimitByEmail(inbounds, email, 2)
		return err
	case "bulk":
		result, _, err := clients.BulkSetEnable(inbounds, []string{email}, false)
		if err != nil {
			return err
		}
		if result.Changed != 1 || len(result.Skipped) != 0 {
			return fmt.Errorf("owner bulk field rejected: %+v", result)
		}
		return nil
	default:
		return fmt.Errorf("unknown field operation %q", operation)
	}
}

// Replaying a stale canonical or ordinary-mirror record would erase the
// credential and restriction edits made after the email lookup.
func TestPasswordProxyOwnerFieldsPreserveCurrentOtherFields(t *testing.T) {
	for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			owner, password, ordinary := passwordUpdateSibling(t)
			var rotated atomic.Bool
			const callback = "test-password-fields-current-values"
			if err := database.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table != "inbounds" || !rotated.CompareAndSwap(false, true) {
					return
				}
				query.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Updates(map[string]any{
						"enable": false, "uuid": "9b248167-038b-418b-9e4d-425d6f38f420", "password": "current-shared-secret",
						"limit_ip": 7, "total_gb": 7777, "expiry_time": int64(1800000000000), "comment": "current-comment",
						"policy_upload_bytes_per_second": 1234, "policy_download_bytes_per_second": 2345, "policy_multiplier": "0.25",
					}).Error; err != nil {
						return err
					}
					var settings map[string]any
					if err := json.Unmarshal([]byte(ordinary.Settings), &settings); err != nil {
						return err
					}
					entry := settings["clients"].([]any)[0].(map[string]any)
					entry["id"], entry["password"], entry["comment"] = "62a97894-52bc-40a5-a9a6-e65cb0528bdc", "current-wire-secret", "current-wire-comment"
					entry["enable"], entry["limitIp"], entry["totalGB"], entry["expiryTime"] = false, 7, 7777, int64(1800000000000)
					encoded, err := json.Marshal(settings)
					if err != nil {
						return err
					}
					return tx.Model(&model.Inbound{}).Where("id = ?", ordinary.Id).Update("settings", string(encoded)).Error
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			if err := passwordOwnerFieldOperation(&ClientService{}, &InboundService{}, owner.Email, operation); err != nil {
				t.Fatal(err)
			}
			current, err := (&ClientService{}).GetByID(owner.Id)
			wantEnable, wantLimit, wantTotal, wantExpiry := false, 7, int64(7777), int64(1800000000000)
			switch operation {
			case "toggle":
				wantEnable = true
			case "ip-limit":
				wantLimit = 3
			case "expiry":
				wantExpiry = 1900000000000
			case "quota":
				wantTotal = 2147483648
			}
			wantPolicy := &model.ClientPolicyOptions{UploadBytesPerSecond: 1234, DownloadBytesPerSecond: 2345, Multiplier: "0.25"}
			if err != nil || !rotated.Load() || current.StableID != owner.StableID || current.UUID != "9b248167-038b-418b-9e4d-425d6f38f420" || current.Password != "current-shared-secret" || current.Comment != "current-comment" || current.Enable != wantEnable || current.LimitIP != wantLimit || current.TotalGB != wantTotal || current.ExpiryTime != wantExpiry || !reflect.DeepEqual(current.Policy, wantPolicy) {
				t.Fatalf("single-field write replayed stale canonical fields: %+v/%v", current, err)
			}
			wire, err := (&InboundService{}).GetInbound(ordinary.Id)
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]any
			if err := json.Unmarshal([]byte(wire.Settings), &settings); err != nil {
				t.Fatal(err)
			}
			entry := settings["clients"].([]any)[0].(map[string]any)
			if entry["id"] != "62a97894-52bc-40a5-a9a6-e65cb0528bdc" || entry["password"] != "current-wire-secret" || entry["comment"] != "current-wire-comment" || entry["enable"] != wantEnable || entry["limitIp"] != float64(wantLimit) || entry["totalGB"] != float64(wantTotal) || entry["expiryTime"] != float64(wantExpiry) {
				t.Fatalf("single-field write replayed stale wire fields: %+v", entry)
			}
			if saved, err := (&InboundService{}).GetInbound(password.Id); err != nil || saved.Settings != password.Settings {
				t.Fatalf("single-field write changed password credentials: %+v/%v", saved, err)
			}
			if history := trafficOf(t, owner.Email); history.Up != 123 || history.Down != 456 {
				t.Fatalf("single-field write reset counters: %+v", history)
			}
		})
	}
}

// Failure after canonical/stat writes must roll back the entire local subset.
func TestPasswordProxyOwnerFieldsSQLRollback(t *testing.T) {
	for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			owner, password, ordinary := passwordUpdateSibling(t)
			before := trafficOf(t, owner.Email)
			injected := errors.New("injected ordinary field persistence failure")
			var failed atomic.Bool
			const callback = "test-password-fields-late-sql-failure"
			if err := database.GetDB().Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "inbounds" && strings.Contains(fmt.Sprint(tx.Statement.Dest), "settings") {
					failed.Store(true)
					tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Update().Remove(callback) })
			err := passwordOwnerFieldOperation(&ClientService{}, &InboundService{}, owner.Email, operation)
			if !failed.Load() || err == nil || !strings.Contains(err.Error(), injected.Error()) {
				t.Fatalf("late local SQL failure not surfaced: %v", err)
			}
			if current, err := (&ClientService{}).GetByID(owner.Id); err != nil || !reflect.DeepEqual(current, &owner) || !reflect.DeepEqual(trafficOf(t, owner.Email), before) {
				t.Fatalf("failed field transaction committed canonical/history: %+v/%v", current, err)
			}
			for _, resource := range []*model.Inbound{password, ordinary} {
				if saved, err := (&InboundService{}).GetInbound(resource.Id); err != nil || saved.Settings != resource.Settings {
					t.Fatalf("failed field transaction committed mirror/accounts: %+v/%v", saved, err)
				}
			}
		})
	}
}

// Saved enable equality is not an acknowledgement. Retry must reconcile the
// saved intent after a prior uncertain runtime result, while keeping no-op
// Changed semantics for Set and successful-record semantics for Bulk.
func TestPasswordProxyOwnerFieldsMatchingEnableReconciles(t *testing.T) {
	for _, operation := range []string{"set", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			owner, _, _ := passwordUpdateSibling(t)
			previous := panelruntime.GetManager()
			t.Cleanup(func() { panelruntime.SetManager(previous) })
			injected := errors.New("field candidate acknowledgement lost")
			var calls atomic.Int32
			panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) {
				if calls.Add(1) == 1 {
					return true, injected
				}
				return true, nil
			}}))
			clients, inbounds := &ClientService{}, &InboundService{}
			if operation == "set" {
				if changed, restart, err := clients.SetClientEnableByEmail(inbounds, owner.Email, false); !errors.Is(err, injected) || changed || !restart {
					t.Fatalf("uncertain set contract=%v/%v/%v", changed, restart, err)
				}
			} else {
				if result, restart, err := clients.BulkSetEnable(inbounds, []string{owner.Email}, false); err != nil || !restart || result.Changed != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, injected.Error()) {
					t.Fatalf("uncertain bulk contract=%+v/%v/%v", result, restart, err)
				}
			}
			if current, err := clients.GetByID(owner.Id); err != nil || current.Enable || trafficOf(t, owner.Email).Enable {
				t.Fatalf("uncertain application discarded saved disable: %+v/%v", current, err)
			}
			if operation == "set" {
				if changed, restart, err := clients.SetClientEnableByEmail(inbounds, owner.Email, false); err != nil || changed || restart {
					t.Fatalf("matching set recovery contract=%v/%v/%v", changed, restart, err)
				}
			} else {
				if result, restart, err := clients.BulkSetEnable(inbounds, []string{owner.Email, owner.Email}, false); err != nil || restart || result.Changed != 1 || len(result.Skipped) != 0 {
					t.Fatalf("matching bulk recovery contract=%+v/%v/%v", result, restart, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("saved equal state did not reconcile: calls=%d", calls.Load())
			}
			if history := trafficOf(t, owner.Email); history.Up != 123 || history.Down != 456 {
				t.Fatalf("retry changed history: %+v", history)
			}
		})
	}
}

func TestPasswordProxyOwnerFieldsScopeBeforeWrites(t *testing.T) {
	for _, drift := range []string{"remote", "malformed-missing-link", "ambiguous-tunnel"} {
		for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
			t.Run(drift+"/"+operation, func(t *testing.T) {
				owner, password, ordinary := passwordUpdateSibling(t)
				switch drift {
				case "remote":
					if err := database.GetDB().Model(ordinary).Update("node_id", 99).Error; err != nil {
						t.Fatal(err)
					}
				case "malformed-missing-link":
					password.Settings = fmt.Sprintf(`{"accounts":[{"user":"alice","pass":17,"ownerClientId":"%s"}],"requireAuthentication":true}`, owner.StableID)
					if err := database.GetDB().Model(password).Update("settings", password.Settings).Error; err != nil {
						t.Fatal(err)
					}
					if err := database.GetDB().Where("client_id = ? AND inbound_id = ?", owner.Id, password.Id).Delete(&model.ClientInbound{}).Error; err != nil {
						t.Fatal(err)
					}
				case "ambiguous-tunnel":
					other := passwordOwner(t, "ambiguous-field-owner")
					tunnel := mkInbound(t, 24574, model.Tunnel, `{"address":"127.0.0.1","port":80,"network":"tcp"}`)
					for _, id := range []int{owner.Id, other.Id} {
						if err := database.GetDB().Create(&model.ClientInbound{ClientId: id, InboundId: tunnel.Id}).Error; err != nil {
							t.Fatal(err)
						}
					}
				}
				before := trafficOf(t, owner.Email)
				if err := passwordOwnerFieldOperation(&ClientService{}, &InboundService{}, owner.Email, operation); err == nil {
					t.Fatal("invalid full owner scope was hidden by field fanout")
				}
				if current, err := (&ClientService{}).GetByID(owner.Id); err != nil || !reflect.DeepEqual(current, &owner) || !reflect.DeepEqual(trafficOf(t, owner.Email), before) {
					t.Fatalf("invalid scope changed canonical/history: %+v/%v", current, err)
				}
				for _, resource := range []*model.Inbound{password, ordinary} {
					var saved model.Inbound
					if err := database.GetDB().First(&saved, resource.Id).Error; err != nil || saved.Settings != resource.Settings {
						t.Fatalf("invalid scope changed resource before fanout: %+v/%v", saved, err)
					}
				}
			})
		}
	}
}

func TestPasswordProxyOwnerFieldsRejectReusedEmail(t *testing.T) {
	for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			owner, password, ordinary := passwordUpdateSibling(t)
			replacement := passwordOwner(t, "fields-replacement")
			if err := database.GetDB().Create(&xray.ClientTraffic{Email: replacement.Email, Enable: true, Up: 33, Down: 44}).Error; err != nil {
				t.Fatal(err)
			}
			var renamed atomic.Bool
			const callback = "test-password-fields-reused-email"
			if err := database.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table != "inbounds" || !renamed.CompareAndSwap(false, true) {
					return
				}
				query.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					for _, change := range []struct {
						id        int
						old, next string
					}{{owner.Id, owner.Email, "fields-current-renamed"}, {replacement.Id, replacement.Email, owner.Email}} {
						if err := tx.Model(&model.ClientRecord{}).Where("id = ?", change.id).Update("email", change.next).Error; err != nil {
							return err
						}
						if err := tx.Model(&xray.ClientTraffic{}).Where("email = ?", change.old).Update("email", change.next).Error; err != nil {
							return err
						}
					}
					return nil
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			err := passwordOwnerFieldOperation(&ClientService{}, &InboundService{}, owner.Email, operation)
			if !renamed.Load() || err == nil || !strings.Contains(err.Error(), ErrManagedConfigStale.Error()) {
				t.Fatalf("old-name replacement escaped identity fence: %v", err)
			}
			if current, err := (&ClientService{}).GetByID(replacement.Id); err != nil || current.Email != owner.Email || current.StableID != replacement.StableID || !current.Enable || current.LimitIP != replacement.LimitIP || current.TotalGB != replacement.TotalGB || current.ExpiryTime != replacement.ExpiryTime {
				t.Fatalf("old-name operation changed replacement: %+v/%v", current, err)
			}
			if history := trafficOf(t, owner.Email); history.Up != 33 || history.Down != 44 || !history.Enable {
				t.Fatalf("old-name operation changed replacement history: %+v", history)
			}
			if current, err := (&ClientService{}).GetByID(owner.Id); err != nil || current.Email != "fields-current-renamed" || !current.Enable || current.LimitIP != owner.LimitIP || current.TotalGB != owner.TotalGB || current.ExpiryTime != owner.ExpiryTime {
				t.Fatalf("stale identity operation changed original: %+v/%v", current, err)
			}
			for _, resource := range []*model.Inbound{password, ordinary} {
				if saved, err := (&InboundService{}).GetInbound(resource.Id); err != nil || saved.Settings != resource.Settings {
					t.Fatalf("stale identity operation changed resource: %+v/%v", saved, err)
				}
			}
		})
	}
}

func TestPasswordProxyOwnerFieldsRejectLateMembership(t *testing.T) {
	for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			owner, _, ordinary := passwordUpdateSibling(t)
			late := mkInbound(t, 24575, model.VLESS, `{"decryption":"none","clients":[]}`)
			var attached atomic.Bool
			const callback = "test-password-fields-late-membership"
			if err := database.GetDB().Callback().Query().After("gorm:query").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table != "client_inbounds" || !reflect.DeepEqual(query.Statement.Selects, []string{"client_id", "inbound_id"}) {
					return
				}
				if _, insideWriter := query.Statement.ConnPool.(gorm.TxCommitter); insideWriter || !attached.CompareAndSwap(false, true) {
					return
				}
				query.AddError(database.GetDB().Create(&model.ClientInbound{ClientId: owner.Id, InboundId: late.Id}).Error)
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			err := passwordOwnerFieldOperation(&ClientService{}, &InboundService{}, owner.Email, operation)
			if !attached.Load() || err == nil || !strings.Contains(err.Error(), ErrManagedConfigStale.Error()) {
				t.Fatalf("late membership escaped graph fence: %v", err)
			}
			if current, err := (&ClientService{}).GetByID(owner.Id); err != nil || !reflect.DeepEqual(current, &owner) {
				t.Fatalf("late membership operation changed canonical record: %+v/%v", current, err)
			}
			if saved, err := (&InboundService{}).GetInbound(ordinary.Id); err != nil || saved.Settings != ordinary.Settings {
				t.Fatalf("late membership operation changed ordinary mirror: %+v/%v", saved, err)
			}
		})
	}
}

func TestPasswordProxyOwnerFieldsBulkPartitionsInvalidOwners(t *testing.T) {
	owner, password, ordinary := passwordUpdateSibling(t)
	legacy := passwordOwner(t, "fields-valid-legacy")
	legacyInbound := mkInbound(t, 24576, model.VLESS, `{"decryption":"none","clients":[]}`)
	if _, err := (&ClientService{}).Attach(&InboundService{}, legacy.Id, []int{legacyInbound.Id}); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(ordinary).Update("node_id", 99).Error; err != nil {
		t.Fatal(err)
	}
	result, _, err := (&ClientService{}).BulkSetEnable(&InboundService{}, []string{owner.Email, legacy.Email, owner.Email, "missing-partition-owner"}, false)
	if err != nil || result.Changed != 1 || len(result.Skipped) != 2 {
		t.Fatalf("partitioned bulk result=%+v/%v", result, err)
	}
	reasons := make(map[string]string)
	for _, skipped := range result.Skipped {
		reasons[skipped.Email] = skipped.Reason
	}
	if reasons[owner.Email] == "" || reasons["missing-partition-owner"] != "client not found" {
		t.Fatalf("invalid owner/missing reports=%+v", result)
	}
	if current, err := (&ClientService{}).GetByID(owner.Id); err != nil || !reflect.DeepEqual(current, &owner) {
		t.Fatalf("partitioned bulk changed invalid owner: %+v/%v", current, err)
	}
	if current, err := (&ClientService{}).GetByID(legacy.Id); err != nil || current.Enable {
		t.Fatalf("partitioned bulk omitted valid legacy owner: %+v/%v", current, err)
	}
	for _, resource := range []*model.Inbound{password, ordinary} {
		var saved model.Inbound
		if err := database.GetDB().First(&saved, resource.Id).Error; err != nil || saved.Settings != resource.Settings {
			t.Fatalf("partitioned bulk wrote invalid owner resource: %+v/%v", saved, err)
		}
	}
}

// A valid sole-owner Tunnel may omit its optional clients mirror entirely.
// Rejecting that form would break every canonical restriction setter.
func TestPasswordProxyOwnerFieldsEmptyTunnelWithoutClients(t *testing.T) {
	for _, operation := range []string{"enable", "toggle", "ip-limit", "expiry", "quota", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			owner, _, _ := passwordUpdateSibling(t)
			clients, inbounds := &ClientService{}, &InboundService{}
			tunnel := mkInbound(t, 24573, model.Tunnel, `{"address":"127.0.0.1","port":80,"network":"tcp"}`)
			if err := database.GetDB().Create(&model.ClientInbound{ClientId: owner.Id, InboundId: tunnel.Id}).Error; err != nil {
				t.Fatal(err)
			}
			if err := passwordOwnerFieldOperation(clients, inbounds, owner.Email, operation); err != nil {
				t.Fatal(err)
			}
			current, err := clients.GetByID(owner.Id)
			if err != nil {
				t.Fatal(err)
			}
			if (operation == "enable" || operation == "toggle" || operation == "bulk") && current.Enable || operation == "ip-limit" && current.LimitIP != 3 || operation == "expiry" && current.ExpiryTime != 1900000000000 || operation == "quota" && current.TotalGB != 2147483648 {
				t.Fatalf("canonical restriction was not saved: %+v", current)
			}
			saved, err := inbounds.GetInbound(tunnel.Id)
			if err != nil || saved.Settings != tunnel.Settings {
				t.Fatalf("optional Tunnel mirror was created or modified: %+v/%v", saved, err)
			}
			var settings map[string]json.RawMessage
			if err := json.Unmarshal([]byte(saved.Settings), &settings); err != nil || settings["clients"] != nil {
				t.Fatalf("unexpected clients mirror: %s/%v", saved.Settings, err)
			}
		})
	}
}
