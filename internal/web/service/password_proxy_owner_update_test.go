package service

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerSingleUpdatePreservesResourceCredentials(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		for _, scope := range []string{"password-only", "ordinary-sibling", "filtered-ordinary"} {
			t.Run(string(protocol)+"/"+scope, func(t *testing.T) {
				setupPolicyLedgerDB(t)
				first, second := passwordOwner(t, "owner-update-first"), passwordOwner(t, "owner-update-second")
				if err := database.GetDB().Create(&xray.ClientTraffic{Email: first.Email, Enable: true, Up: 123, Down: 456}).Error; err != nil {
					t.Fatal(err)
				}
				total := model.ClientPolicyTotal{ClientID: first.StableID, RawUpload: 123, RawDownload: 456, BilledBytes: 777}
				if err := database.GetDB().Create(&total).Error; err != nil {
					t.Fatal(err)
				}
				svc, clients := &InboundService{}, &ClientService{}
				ib, _, err := svc.AddInbound(&model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 24550, Settings: passwordOwnerSettings(t, protocol,
					map[string]any{"user": "alice", "pass": "resource-one", "ownerClientId": first.StableID},
					map[string]any{"user": "ALICE", "pass": "resource-alias", "ownerClientId": first.StableID},
					map[string]any{"user": "bob", "pass": "resource-two", "ownerClientId": second.StableID})})
				if err != nil {
					t.Fatal(err)
				}
				var filter []int
				var ordinary *model.Inbound
				if scope != "password-only" {
					ordinary = mkInbound(t, 24551, model.VLESS, `{"decryption":"none","clients":[]}`)
					if _, err := clients.Attach(svc, first.Id, []int{ordinary.Id}); err != nil {
						t.Fatal(err)
					}
					if scope == "filtered-ordinary" {
						filter = []int{ordinary.Id}
					}
				}
				beforeLinks := linksOf(t, ib.Id)
				current, err := clients.GetByID(first.Id)
				if err != nil {
					t.Fatal(err)
				}
				updated := *current.ToClient()
				updated.Email = "owner-update-renamed"
				updated.SubID = "owner-update-new-sub"
				updated.ID = uuid.NewString()
				updated.Password = "shared-canonical-rotated"
				updated.TotalGB = 987654
				updated.ExpiryTime = time.Now().Add(24 * time.Hour).UnixMilli()
				updated.Enable = false
				updated.LimitIP = 3
				updated.Comment = "updated canonical comment"
				updated.Policy = &model.ClientPolicyOptions{UploadBytesPerSecond: 1234, DownloadBytesPerSecond: 2345, Multiplier: "0.5"}
				if _, err := clients.Update(svc, first.Id, updated, 4, filter...); err != nil {
					t.Fatalf("canonical owner update rejected: %v", err)
				}
				after, err := clients.GetByID(first.Id)
				if err != nil {
					t.Fatal(err)
				}
				if after.StableID != first.StableID || after.Email != updated.Email || after.SubID != updated.SubID || after.UUID != updated.ID || after.Password != updated.Password || after.TotalGB != updated.TotalGB || after.ExpiryTime != updated.ExpiryTime || after.Enable || after.LimitIP != 3 || after.LimitHwid != 4 || after.Comment != updated.Comment || !reflect.DeepEqual(after.Policy, updated.Policy) {
					t.Fatalf("canonical owner fields not saved: %+v", after)
				}
				var saved model.Inbound
				if err := database.GetDB().First(&saved, ib.Id).Error; err != nil {
					t.Fatal(err)
				}
				if saved.Settings != ib.Settings || !reflect.DeepEqual(linksOf(t, saved.Id), beforeLinks) {
					t.Fatal("shared update rewrote resource credentials or links")
				}
				if got, err := clients.GetByID(second.Id); err != nil || !reflect.DeepEqual(got, &second) {
					t.Fatalf("shared update changed another owner: %+v %v", got, err)
				}
				if usage := trafficOf(t, updated.Email); usage.Up != 123 || usage.Down != 456 || usage.Total != updated.TotalGB || usage.ExpiryTime != updated.ExpiryTime || usage.Enable {
					t.Fatalf("canonical history/quota rename: %+v", usage)
				}
				if ledger := policyLedgerTotal(t, first.StableID); ledger.RawUpload != 123 || ledger.RawDownload != 456 || ledger.BilledBytes != 777 {
					t.Fatalf("shared update reset lifetime ledger: %+v", ledger)
				}
				if ordinary != nil {
					current, err := svc.GetInbound(ordinary.Id)
					if err != nil {
						t.Fatal(err)
					}
					entries, err := svc.GetClients(current)
					if err != nil || len(entries) != 1 || entries[0].Email != updated.Email || entries[0].ID != updated.ID {
						t.Fatalf("ordinary sibling not updated: %+v %v", entries, err)
					}
				}
			})
		}
	}
}
