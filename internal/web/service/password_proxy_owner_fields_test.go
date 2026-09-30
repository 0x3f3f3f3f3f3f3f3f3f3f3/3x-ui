package service

import (
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerFieldsPublicEntrypoints(t *testing.T) {
	for _, protocol := range []model.Protocol{model.HTTP, model.Mixed} {
		for _, scope := range []string{"password-only", "ordinary", "empty-tunnel"} {
			for _, operation := range []string{"read-enabled", "read-disabled", "set-disable", "toggle-disable", "ip-limit", "expiry", "quota", "bulk-disable", "bulk-enable"} {
				t.Run(string(protocol)+"/"+scope+"/"+operation, func(t *testing.T) {
					setupPolicyLedgerDB(t)
					first, second := passwordOwner(t, "fields-first"), passwordOwner(t, "fields-second")
					inbounds, clients := &InboundService{}, &ClientService{}
					if operation == "read-disabled" || operation == "bulk-enable" {
						if err := database.GetDB().Model(&first).Update("enable", false).Error; err != nil {
							t.Fatal(err)
						}
					}
					if err := database.GetDB().Create(&xray.ClientTraffic{Email: first.Email, Enable: first.Enable, Up: 123, Down: 456, Total: first.TotalGB, ExpiryTime: first.ExpiryTime}).Error; err != nil {
						t.Fatal(err)
					}
					if !first.Enable {
						if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", first.Email).Update("enable", false).Error; err != nil {
							t.Fatal(err)
						}
					}
					if err := database.GetDB().Create(&model.ClientPolicyTotal{ClientID: first.StableID, RawUpload: 123, RawDownload: 456, BilledBytes: 777}).Error; err != nil {
						t.Fatal(err)
					}
					password, _, err := inbounds.AddInbound(&model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 24570, Settings: passwordOwnerSettings(t, protocol,
						map[string]any{"user": "alice", "pass": "resource-one", "ownerClientId": first.StableID},
						map[string]any{"user": "ALICE", "pass": "resource-alias", "ownerClientId": first.StableID},
						map[string]any{"user": first.Email, "pass": "other-resource", "ownerClientId": second.StableID})})
					if err != nil {
						t.Fatal(err)
					}
					switch scope {
					case "ordinary":
						sibling := mkInbound(t, 24571, model.VLESS, `{"decryption":"none","clients":[]}`)
						if _, err := clients.Attach(inbounds, first.Id, []int{sibling.Id}); err != nil {
							t.Fatal(err)
						}
					case "empty-tunnel":
						sibling := mkInbound(t, 24571, model.Tunnel, `{"address":"127.0.0.1","port":80,"network":"tcp","clients":[]}`)
						if err := database.GetDB().Create(&model.ClientInbound{ClientId: first.Id, InboundId: sibling.Id}).Error; err != nil {
							t.Fatal(err)
						}
					}
					beforeLinks := linksOf(t, password.Id)
					expected := first
					switch operation {
					case "read-enabled", "read-disabled":
						value, readErr := clients.CheckIsEnabledByEmail(inbounds, first.Email)
						if readErr != nil || value != first.Enable {
							t.Fatalf("canonical enable read=%v/%v want %v", value, readErr, first.Enable)
						}
					case "set-disable":
						changed, _, applyErr := clients.SetClientEnableByEmail(inbounds, first.Email, false)
						if applyErr != nil || !changed {
							t.Fatalf("set-disable=%v/%v", changed, applyErr)
						}
						expected.Enable = false
					case "toggle-disable":
						value, _, applyErr := clients.ToggleClientEnableByEmail(inbounds, first.Email)
						if applyErr != nil || value {
							t.Fatalf("toggle-disable=%v/%v", value, applyErr)
						}
						expected.Enable = false
					case "ip-limit":
						_, err = clients.ResetClientIpLimitByEmail(inbounds, first.Email, 3)
						expected.LimitIP = 3
					case "expiry":
						_, err = clients.ResetClientExpiryTimeByEmail(inbounds, first.Email, 1900000000000)
						expected.ExpiryTime = 1900000000000
					case "quota":
						_, err = clients.ResetClientTrafficLimitByEmail(inbounds, first.Email, 2)
						expected.TotalGB = 2 * 1024 * 1024 * 1024
					case "bulk-disable", "bulk-enable":
						enable := operation == "bulk-enable"
						result, _, applyErr := clients.BulkSetEnable(inbounds, []string{first.Email, first.Email, "missing-fields-owner"}, enable)
						if applyErr != nil || result.Changed != 1 || len(result.Skipped) != 1 || result.Skipped[0].Email != "missing-fields-owner" {
							t.Fatalf("bulk result=%+v/%v", result, applyErr)
						}
						expected.Enable = enable
					}
					if err != nil {
						t.Fatal(err)
					}
					got, err := clients.GetByID(first.Id)
					if err != nil || got.StableID != first.StableID || got.UUID != first.UUID || got.Password != first.Password || got.Enable != expected.Enable || got.LimitIP != expected.LimitIP || got.TotalGB != expected.TotalGB || got.ExpiryTime != expected.ExpiryTime || !reflect.DeepEqual(got.Policy, first.Policy) {
						t.Fatalf("canonical field persistence=%+v/%v", got, err)
					}
					var saved model.Inbound
					if err := database.GetDB().First(&saved, password.Id).Error; err != nil || saved.Settings != password.Settings || !reflect.DeepEqual(linksOf(t, password.Id), beforeLinks) {
						t.Fatalf("field write changed password credentials/membership: %+v/%v", saved, err)
					}
					if got, err := clients.GetByID(second.Id); err != nil || !reflect.DeepEqual(got, &second) {
						t.Fatalf("field write changed wire-name collision owner: %+v/%v", got, err)
					}
					if usage := trafficOf(t, first.Email); usage.Up != 123 || usage.Down != 456 || usage.Enable != expected.Enable || usage.Total != expected.TotalGB || usage.ExpiryTime != expected.ExpiryTime {
						t.Fatalf("field write changed history or missed restriction: %+v", usage)
					}
					if usage := policyLedgerTotal(t, first.StableID); usage.RawUpload != 123 || usage.RawDownload != 456 || usage.BilledBytes != 777 {
						t.Fatalf("field write changed lifetime ledger: %+v", usage)
					}
				})
			}
		}
	}
}
