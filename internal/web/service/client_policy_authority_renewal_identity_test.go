package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestManagedAuthorityRenewalRecoveryPreservesUUIDAndCurrentAttachments(t *testing.T) {
	for _, kind := range []string{"renamed-email-reused", "deleted-id-email-reused", "new-attachment"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAuthorityRenewalRecoveryFixture(t, false)
			restoreAuthorityRenewalSQL(t, f)
			db := database.GetDB()
			var replacement model.ClientRecord
			var alias *model.Inbound
			var old model.Inbound
			if err := db.First(&old, f.inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			if kind == "deleted-id-email-reused" {
				if err := db.Delete(&model.ClientRecord{}, "stable_id = ?", f.client.StableID).Error; err != nil {
					t.Fatal(err)
				}
				replacement = model.ClientRecord{Id: f.client.Id, Email: f.client.Email, ExpiryTime: 9007199254740993, Enable: true}
			} else if kind == "renamed-email-reused" {
				if err := db.Table("clients").Where("stable_id = ?", f.client.StableID).Update("email", "renewal-renamed-current").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", f.client.Email).Update("email", "renewal-renamed-current").Error; err != nil {
					t.Fatal(err)
				}
				replacement = model.ClientRecord{Email: f.client.Email, ExpiryTime: 9007199254740993, Enable: true}
			}
			if kind != "new-attachment" {
				if err := db.Create(&replacement).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Where("email = ?", f.client.Email).Delete(&xray.ClientTraffic{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&xray.ClientTraffic{Email: replacement.Email, ExpiryTime: replacement.ExpiryTime, ResetCount: 7, Up: 9, Down: 10}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if kind != "deleted-id-email-reused" {
				if err := db.Delete(&model.ClientInbound{}, "client_id = ?", f.client.Id).Error; err != nil {
					t.Fatal(err)
				}
				var current model.ClientRecord
				if err := db.First(&current, "stable_id = ?", f.client.StableID).Error; err != nil {
					t.Fatal(err)
				}
				settings := fmt.Sprintf(`{"clients":[{"clientId":%q,"email":%q,"snellPsk":"current-native-credential","updated_at":%d,"expiryTime":1,"exact":9007199254740993}],"marker":"preserve"}`, current.StableID, current.Email, f.snapshot.ResetAt+10000)
				alias = mkInbound(t, 29111, model.Snell, settings)
				if err := db.Create(&model.ClientInbound{ClientId: current.Id, InboundId: alias.Id}).Error; err != nil {
					t.Fatal(err)
				}
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			boot := policyauthority.NodeBoot{NodeID: "local", SourceID: f.config.InstanceID, BootID: "renewal-identity-held-boot"}
			if err := state.Journal.RegisterBoot(boot); err != nil {
				t.Fatal(err)
			}
			grant, err := state.Journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: state.Journal.Identity(), NodeBoot: boot, ClientID: f.client.StableID, WindowID: f.account.Policy.WindowID, PolicyVersion: f.account.Policy.Version}, RequestID: "renewal-identity-held40", ChallengeID: "renewal-identity-held-challenge", Capacity: 40, Upload: f.account.Policy.Upload, Download: f.account.Policy.Download, LeaseDuration: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			before, err := state.Journal.Account(f.client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			if err := recoverAuthorityResetCaptures(context.Background(), db, state.Journal, f.config.InstanceID); err != nil {
				t.Fatal(err)
			}
			after, err := state.Journal.Account(f.client.StableID)
			if err != nil || before != after {
				t.Fatalf("identity recovery changed funded account: %v", err)
			}
			retained, err := state.Journal.Grant(grant.GrantID)
			if err != nil || retained != grant {
				t.Fatalf("identity recovery changed finite40 grant: %v", err)
			}
			if replacement.StableID != "" {
				var current model.ClientRecord
				if err := db.First(&current, "stable_id = ?", replacement.StableID).Error; err != nil {
					t.Fatal(err)
				}
				row := trafficOf(t, replacement.Email)
				if current.ExpiryTime != replacement.ExpiryTime || row.ExpiryTime != replacement.ExpiryTime || row.ResetCount != 7 || row.Up != 9 || row.Down != 10 {
					t.Fatalf("old renewal transferred to replacement identity: %d/%+v", current.ExpiryTime, row)
				}
				var count int64
				if err := db.Model(&model.ClientPolicyReset{}).Where("client_id = ?", replacement.StableID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("replacement acquired old semantic window: %d/%v", count, err)
				}
			}
			var untouched model.Inbound
			if err := db.First(&untouched, old.Id).Error; err != nil {
				t.Fatal(err)
			}
			if untouched.Settings != old.Settings {
				t.Fatal("renewal replay changed a detached or reused numeric attachment")
			}
			if alias != nil {
				if err := db.First(alias, alias.Id).Error; err != nil {
					t.Fatal(err)
				}
				var payload struct {
					Marker  string
					Clients []struct {
						ClientID string `json:"clientId"`
						PSK      string `json:"snellPsk"`
						Updated  int64  `json:"updated_at"`
						Expiry   int64  `json:"expiryTime"`
						Exact    int64
					}
				}
				if err := json.Unmarshal([]byte(alias.Settings), &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.Clients) != 1 || payload.Marker != "preserve" {
					t.Fatal("recovery lost new current attachment")
				}
				entry := payload.Clients[0]
				if entry.ClientID != f.client.StableID || entry.PSK != "current-native-credential" || entry.Updated != f.snapshot.ResetAt+10000 || entry.Expiry != f.snapshot.Effects[0].AfterExpiryTime || entry.Exact != 9007199254740993 {
					t.Fatalf("recovery overwrote current native mirror: %+v", entry)
				}
			}
		})
	}
}
