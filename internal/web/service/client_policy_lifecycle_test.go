package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyLifecycleKeepsManagedRestrictions(t *testing.T) {
	for _, kind := range []string{"quota", "expiry", "renewal", "first-use"} {
		t.Run(kind, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			client := model.Client{Email: "managed-" + kind, ID: "11111111-1111-1111-1111-111111111111", Enable: true, TotalGB: 100}
			switch kind {
			case "expiry":
				client.ExpiryTime = time.Now().Add(-time.Hour).UnixMilli()
			case "renewal":
				client.ExpiryTime = time.Now().Add(-time.Hour).UnixMilli()
				client.Reset = 30
				client.Enable = false
			case "first-use":
				client.ExpiryTime = -86400000
			}
			inbound := mkInbound(t, 24217, model.VLESS, clientsSettings(t, []model.Client{client}))
			if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
				t.Fatal(err)
			}
			var record model.ClientRecord
			if err := db.First(&record, "email = ?", client.Email).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&record).Update("enable", client.Enable).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: client.Email, InboundId: inbound.Id, Enable: client.Enable, Total: client.TotalGB, Up: 111, Down: 222, ExpiryTime: client.ExpiryTime, Reset: client.Reset}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.ClientPolicyTotal{ClientID: record.StableID, RawUpload: 111, RawDownload: 222, BilledBytes: 50}).Error; err != nil {
				t.Fatal(err)
			}
			if _, _, err := (&InboundService{}).AddTraffic(nil, []*xray.ClientTraffic{{Email: client.Email, Up: 1}}); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&record, record.Id).Error; err != nil {
				t.Fatal(err)
			}
			row := trafficOf(t, client.Email)
			if record.Enable != client.Enable || row.Enable != client.Enable || record.ExpiryTime != client.ExpiryTime || row.ExpiryTime != client.ExpiryTime || row.Up != 112 || row.Down != 222 || row.ResetCount != 0 {
				t.Fatalf("legacy lifecycle rewrote a prepared managed client: record=%+v traffic=%+v", record, row)
			}
			if err := db.First(inbound, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			var settings struct {
				Clients []model.Client `json:"clients"`
			}
			if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
				t.Fatal(err)
			}
			if len(settings.Clients) != 1 || settings.Clients[0].Enable != client.Enable || settings.Clients[0].ExpiryTime != client.ExpiryTime {
				t.Fatalf("legacy lifecycle changed managed inbound configuration: %s", inbound.Settings)
			}
			if got := policyLedgerTotal(t, record.StableID); got.RawUpload != 111 || got.RawDownload != 222 || got.BilledBytes != 50 {
				t.Fatalf("legacy stats changed committed ledger: %+v", got)
			}
		})
	}
}

func TestClientPolicyLifecyclePreservesManagedSiblingPrecision(t *testing.T) {
	for _, action := range []string{"renewal", "first-use", "disable"} {
		t.Run(action, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			managed := model.Client{Email: "managed-sibling", ID: "11111111-1111-1111-1111-111111111111", Enable: true, TotalGB: 9007199254740993}
			legacy := model.Client{Email: "legacy-sibling", ID: "22222222-2222-2222-2222-222222222222", Enable: true, TotalGB: 1000}
			switch action {
			case "renewal":
				legacy.ExpiryTime = time.Now().Add(-time.Hour).UnixMilli()
				legacy.Reset = 1
			case "first-use":
				legacy.ExpiryTime = -86400000
			case "disable":
				legacy.TotalGB = 1
			}
			raw, err := json.Marshal(map[string]any{"clients": []model.Client{managed, legacy}})
			if err != nil {
				t.Fatal(err)
			}
			inbound := mkInbound(t, 24219, model.VLESS, string(raw))
			if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{managed, legacy}); err != nil {
				t.Fatal(err)
			}
			var owner model.ClientRecord
			if err := db.First(&owner, "email = ?", managed.Email).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.ClientPolicyTotal{ClientID: owner.StableID}).Error; err != nil {
				t.Fatal(err)
			}
			for _, client := range []model.Client{managed, legacy} {
				if err := db.Create(&xray.ClientTraffic{Email: client.Email, InboundId: inbound.Id, Enable: true, Total: client.TotalGB, ExpiryTime: client.ExpiryTime, Reset: client.Reset}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := (&InboundService{}).AddTraffic(nil, []*xray.ClientTraffic{{Email: legacy.Email, Up: 2}}); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&owner, owner.Id).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(inbound, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			clients, err := ParseInboundSettingsClients(inbound.Settings)
			if err != nil {
				t.Fatal(err)
			}
			if owner.TotalGB != managed.TotalGB || len(clients) != 2 || clients[0].TotalGB != managed.TotalGB {
				t.Fatalf("legacy sibling maintenance rounded a managed quota: record=%d settings=%s", owner.TotalGB, inbound.Settings)
			}
			row := trafficOf(t, legacy.Email)
			if action == "renewal" && (row.ResetCount != 1 || row.ExpiryTime <= time.Now().UnixMilli()) || action == "first-use" && row.ExpiryTime <= time.Now().UnixMilli() || action == "disable" && row.Enable {
				t.Fatalf("precision protection skipped legacy lifecycle work: %+v", row)
			}
		})
	}
}
