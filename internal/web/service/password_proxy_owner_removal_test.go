package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerRemovalEntrypoints(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		for _, action := range []string{"detach", "bulk-detach", "delete", "bulk-delete"} {
			t.Run(fmt.Sprintf("%s/%s", protocol, action), func(t *testing.T) {
				setupPolicyLedgerDB(t)
				first, second := passwordOwner(t, "removal-first"), passwordOwner(t, "removal-second")
				if err := database.GetDB().Model(&second).Update("enable", false).Error; err != nil {
					t.Fatal(err)
				}
				second.Enable = false
				retained := map[string]any{"user": first.Email, "pass": "second-resource", "ownerClientId": second.StableID, "note": "retained metadata"}
				ibSvc, clients := &InboundService{}, &ClientService{}
				primary, _, err := ibSvc.AddInbound(&model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 24531, Settings: passwordOwnerSettings(t, protocol,
					map[string]any{"user": "alice", "pass": "resource-first", "ownerClientId": first.StableID},
					map[string]any{"user": "ALICE", "pass": "resource-alias", "ownerClientId": first.StableID}, retained)})
				if err != nil {
					t.Fatal(err)
				}
				sibling, _, err := ibSvc.AddInbound(&model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 24532, Settings: passwordOwnerSettings(t, protocol,
					map[string]any{"user": "sibling-first", "pass": "sibling-resource", "ownerClientId": first.StableID})})
				if err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", first.Email).Updates(map[string]any{"up": 123, "down": 456, "inbound_id": primary.Id}).Error; err != nil {
					t.Fatal(err)
				}
				total := model.ClientPolicyTotal{ClientID: first.StableID, RawUpload: 123, RawDownload: 456, BilledBytes: 777}
				if err := database.GetDB().Create(&total).Error; err != nil {
					t.Fatal(err)
				}
				switch action {
				case "detach":
					_, err = clients.Detach(ibSvc, first.Id, []int{primary.Id})
				case "bulk-detach":
					var result *BulkDetachResult
					result, _, err = clients.BulkDetach(ibSvc, []string{first.Email}, []int{primary.Id})
					if err == nil && (len(result.Detached) != 1 || len(result.Errors) != 0 || len(result.Skipped) != 0) {
						t.Fatalf("bulk detach result: %+v", result)
					}
				case "delete":
					_, err = clients.Delete(ibSvc, first.Id, true)
				case "bulk-delete":
					var result BulkDeleteResult
					result, _, err = clients.BulkDelete(ibSvc, []string{first.Email}, true)
					if err == nil && (result.Deleted != 1 || len(result.Skipped) != 0) {
						t.Fatalf("bulk delete result: %+v", result)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := ibSvc.GetInbound(primary.Id)
				if err != nil {
					t.Fatal(err)
				}
				var settings map[string]any
				if err := json.Unmarshal([]byte(got.Settings), &settings); err != nil {
					t.Fatal(err)
				}
				if accounts, ok := settings["accounts"].([]any); !ok || len(accounts) != 1 || !reflect.DeepEqual(accounts[0], retained) {
					t.Fatalf("wrong accounts after %s: %s", action, got.Settings)
				}
				if _, exists := settings["clients"]; exists {
					t.Fatal("removal introduced shared credential mirrors")
				}
				if links := linksOf(t, primary.Id); len(links) != 1 || links[second.Id].ClientId != second.Id {
					t.Fatalf("wrong primary memberships: %+v", links)
				}
				var savedSecond model.ClientRecord
				if err := database.GetDB().First(&savedSecond, second.Id).Error; err != nil || !reflect.DeepEqual(savedSecond, second) {
					t.Fatalf("removal rewrote sibling canonical record: %+v %v", savedSecond, err)
				}
				deleting := action == "delete" || action == "bulk-delete"
				var remaining, tombstones int64
				if err := database.GetDB().Model(&model.ClientRecord{}).Where("id = ?", first.Id).Count(&remaining).Error; err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Model(&model.ClientPolicyTombstone{}).Where("client_id = ?", first.StableID).Count(&tombstones).Error; err != nil {
					t.Fatal(err)
				}
				if deleting && (remaining != 0 || tombstones != 1 || len(linksOf(t, sibling.Id)) != 0) || !deleting && (remaining != 1 || tombstones != 0 || len(linksOf(t, sibling.Id)) != 1) {
					t.Fatalf("removal identity outcome: deleting=%t remaining=%d tombstones=%d", deleting, remaining, tombstones)
				}
				traffic := trafficOf(t, first.Email)
				if traffic.Up != 123 || traffic.Down != 456 || deleting && traffic.InboundId != 0 || !deleting && traffic.InboundId != sibling.Id {
					t.Fatalf("removal changed/misattributed shared history: %+v", traffic)
				}
				if actual := policyLedgerTotal(t, first.StableID); !reflect.DeepEqual(actual, total) {
					t.Fatalf("removal changed stable ledger: %+v", actual)
				}
				if !deleting {
					if _, err := clients.Detach(ibSvc, first.Id, []int{primary.Id}); err != nil {
						t.Fatalf("repeat detach: %v", err)
					}
				}
				if _, err := clients.Detach(ibSvc, second.Id, []int{primary.Id}); err != nil {
					t.Fatal(err)
				}
				got, err = ibSvc.GetInbound(primary.Id)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(got.Settings), &settings); err != nil {
					t.Fatal(err)
				}
				if accounts, ok := settings["accounts"].([]any); !ok || len(accounts) != 0 || protocol == model.HTTP && settings["requireAuthentication"] != true || protocol == model.Mixed && settings["auth"] != "password" {
					t.Fatalf("last owner removal reopened authentication: %s", got.Settings)
				}
			})
		}
	}
}
