package service

import (
	"reflect"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerSingleUpdateOmissionAndClearing(t *testing.T) {
	setupPolicyLedgerDB(t)
	first, second := passwordOwner(t, "update-fields-first"), passwordOwner(t, "update-fields-second")
	// Existing subscriptions may be shared; an unchanged ID must stay valid.
	if err := database.GetDB().Model(&second).Update("sub_id", first.SubID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: first.Email, Enable: true, Up: 123, Down: 456}).Error; err != nil {
		t.Fatal(err)
	}
	svc, clients := &InboundService{}, &ClientService{}
	ib, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24552, Settings: passwordOwnerSettings(t, model.HTTP,
		map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": first.StableID},
		map[string]any{"user": "bob", "pass": "other-secret", "ownerClientId": second.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	updated := *first.ToClient()
	updated.Policy, updated.ID, updated.Password = nil, "", ""
	updated.TotalGB, updated.ExpiryTime, updated.Comment = 0, 0, ""
	if _, err := clients.Update(svc, first.Id, updated, 0); err != nil {
		t.Fatalf("omission/clearing update rejected: %v", err)
	}
	current, err := clients.GetByID(first.Id)
	if err != nil || current.TotalGB != 0 || current.ExpiryTime != 0 || current.Comment != "" || current.UUID != first.UUID || current.Password != first.Password || !reflect.DeepEqual(current.Policy, first.Policy) {
		t.Fatalf("omission/clearing did not preserve canonical fields: %+v %v", current, err)
	}
	updated.Policy = &model.ClientPolicyOptions{Multiplier: "1"}
	if _, err := clients.Update(svc, first.Id, updated, 0); err != nil {
		t.Fatal(err)
	}
	current, err = clients.GetByID(first.Id)
	if err != nil || !reflect.DeepEqual(current.Policy, updated.Policy) {
		t.Fatalf("explicit policy clearing was ignored: %+v %v", current, err)
	}
	var saved model.Inbound
	if err := database.GetDB().First(&saved, ib.Id).Error; err != nil || saved.Settings != ib.Settings {
		t.Fatalf("shared field clearing changed accounts: %+v %v", saved, err)
	}
	if usage := trafficOf(t, first.Email); usage.Up != 123 || usage.Down != 456 || usage.Total != 0 || usage.ExpiryTime != 0 {
		t.Fatalf("field clearing changed history: %+v", usage)
	}
}

func TestPasswordProxyOwnerSingleUpdateInheritsCurrentPolicy(t *testing.T) {
	for _, sibling := range []bool{false, true} {
		t.Run(map[bool]string{false: "password-only", true: "ordinary-sibling"}[sibling], func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "update-current-policy")
			svc, clients := &InboundService{}, &ClientService{}
			_, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24553, Settings: passwordOwnerSettings(t, model.HTTP,
				map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": owner.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			if sibling {
				ordinary := mkInbound(t, 24554, model.VLESS, `{"decryption":"none","clients":[]}`)
				if _, err := clients.Attach(svc, owner.Id, []int{ordinary.Id}); err != nil {
					t.Fatal(err)
				}
			}
			latest := &model.ClientPolicyOptions{UploadBytesPerSecond: 1234, DownloadBytesPerSecond: 2345, Multiplier: "0.25"}
			var changed atomic.Bool
			const callback = "test-password-update-current-policy"
			if err := database.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table == "inbounds" && changed.CompareAndSwap(false, true) {
					query.AddError(database.GetDB().Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Updates(map[string]any{
						"policy_upload_bytes_per_second": latest.UploadBytesPerSecond, "policy_download_bytes_per_second": latest.DownloadBytesPerSecond, "policy_multiplier": latest.Multiplier,
					}).Error)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			updated := *owner.ToClient()
			updated.Policy, updated.Comment = nil, "metadata-only"
			if _, err := clients.Update(svc, owner.Id, updated, 0); err != nil {
				t.Fatal(err)
			}
			current, err := clients.GetByID(owner.Id)
			if err != nil || !changed.Load() || !reflect.DeepEqual(current.Policy, latest) {
				t.Fatalf("omission restored the stale policy: %+v %v", current, err)
			}
		})
	}
}
