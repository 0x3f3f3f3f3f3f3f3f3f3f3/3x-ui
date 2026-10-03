package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
)

func TestAuthorityRenewalMirrorsPreserveCurrentIdentityAndStamps(t *testing.T) {
	for _, kind := range []string{"ordinary", "foreign-uuid", "password", "empty-tunnel"} {
		t.Run(kind, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			client := model.ClientRecord{Email: "renewal-mirror-owner", ExpiryTime: 9007199254740993, UpdatedAt: 9000, Enable: true}
			if err := db.Create(&client).Error; err != nil {
				t.Fatal(err)
			}
			protocol := model.VLESS
			id := client.StableID
			if kind == "foreign-uuid" {
				id = uuid.NewString()
			}
			settings := fmt.Sprintf(`{"marker":9007199254740993,"clients":[{"clientId":%q,"email":%q,"id":"current-credential","password":"later-password","expiryTime":1,"updated_at":12000,"unknown":{"exact":9007199254740993}}]}`, id, client.Email)
			if kind == "password" {
				protocol, settings = model.HTTP, `{"accounts":[{"user":"preserve","pass":"current-password"}],"marker":9007199254740993}`
			}
			if kind == "empty-tunnel" {
				protocol, settings = model.Tunnel, `{"address":"127.0.0.1","port":1234,"network":"tcp","clients":[],"marker":9007199254740993}`
			}
			inbound := mkInbound(t, 29318, protocol, settings)
			if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id}).Error; err != nil {
				t.Fatal(err)
			}
			err := runSerializedTx(func(tx *gorm.DB) error {
				return updateManagedRenewalInbounds(tx, map[string]int64{client.Email: client.ExpiryTime}, 1000)
			})
			if kind == "foreign-uuid" {
				if !errors.Is(err, ErrClientPolicyLedger) {
					t.Fatalf("renewal mirror accepted another UUID: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var current model.Inbound
			if err := db.First(&current, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			if kind != "ordinary" {
				if current.Settings != settings {
					t.Fatalf("renewal mirror changed credentials/empty settings: %s", current.Settings)
				}
				return
			}
			var payload struct {
				Marker  int64
				Clients []struct {
					ClientID string `json:"clientId"`
					ID       string
					Password string
					Expiry   int64 `json:"expiryTime"`
					Updated  int64 `json:"updated_at"`
					Unknown  struct{ Exact int64 }
				}
			}
			if err := json.Unmarshal([]byte(current.Settings), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Clients) != 1 {
				t.Fatal("renewal mirror lost current attachment")
			}
			alias := payload.Clients[0]
			if payload.Marker != 9007199254740993 || alias.Unknown.Exact != payload.Marker || alias.ClientID != client.StableID || alias.ID != "current-credential" || alias.Password != "later-password" || alias.Expiry != client.ExpiryTime || alias.Updated != 12000 {
				t.Fatalf("renewal mirror lost current identity/exact values/monotone stamp: %+v", payload)
			}
		})
	}
}
