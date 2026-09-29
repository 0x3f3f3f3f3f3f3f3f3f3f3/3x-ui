package service

import (
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestFirstUseTrafficSettlementRollsBackEveryWrite(t *testing.T) {
	for _, stage := range []string{"inbound", "client", "expiry"} {
		t.Run(stage, func(t *testing.T) {
			setupBulkDB(t)
			db := database.GetDB()
			client := model.Client{Email: "first-use-atomic", ID: "ce8d33df-3a64-4f10-8f9b-91c3a8e0d001", Enable: true, ExpiryTime: -86400000}
			inbound := mkInbound(t, 45001, model.VLESS, clientsSettings(t, []model.Client{client}))
			svc := &InboundService{}
			if err := svc.clientService.SyncInbound(db, inbound.Id, []model.Client{client}); err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: client.Email, InboundId: inbound.Id, Enable: true, ExpiryTime: client.ExpiryTime, Up: 100, Down: 200}).Error; err != nil {
				t.Fatal(err)
			}
			injected := errors.New("first use write failed")
			const name = "test:first-use-write-failure"
			register, remove := db.Callback().Update().Before("gorm:update").Register, db.Callback().Update().Remove
			if stage == "inbound" {
				register, remove = db.Callback().Create().Before("gorm:create").Register, db.Callback().Create().Remove
			}
			if stage == "expiry" {
				register, remove = db.Callback().Raw().Before("gorm:raw").Register, db.Callback().Raw().Remove
			}
			hits := 0
			if err := register(name, func(tx *gorm.DB) {
				if stage == "inbound" && tx.Statement.Table == "inbounds" || stage == "client" && tx.Statement.Table == "clients" || stage == "expiry" && strings.HasPrefix(tx.Statement.SQL.String(), "UPDATE client_traffics SET expiry_time") {
					hits++
					tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = remove(name) })
			apply := func() error {
				return db.Transaction(func(tx *gorm.DB) error {
					return svc.addClientTraffic(tx, []*xray.ClientTraffic{{Email: client.Email, Up: 4, Down: 4}})
				})
			}
			if err := apply(); !errors.Is(err, injected) {
				t.Errorf("settlement swallowed %s write failure: %v", stage, err)
			}
			if hits == 0 {
				t.Fatal("failure injection did not reach the requested write")
			}
			var stored xray.ClientTraffic
			if err := db.Where("email = ?", client.Email).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Up != 100 || stored.Down != 200 || stored.ExpiryTime != client.ExpiryTime {
				t.Errorf("failed settlement partially committed: %+v", stored)
			}
			var record model.ClientRecord
			if err := db.Where("email = ?", client.Email).First(&record).Error; err != nil {
				t.Fatal(err)
			}
			if record.ExpiryTime != client.ExpiryTime {
				t.Errorf("failed settlement changed canonical expiry: %d", record.ExpiryTime)
			}
			if err := remove(name); err != nil {
				t.Fatal(err)
			}
			if err := apply(); err != nil {
				t.Fatal(err)
			}
			if err := db.Where("email = ?", client.Email).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Up != 104 || stored.Down != 204 || stored.ExpiryTime <= 0 {
				t.Fatalf("retry did not commit usage and expiry together: %+v", stored)
			}
		})
	}
}

func TestFirstUseTrafficSettlementDoesNotStartIdleClients(t *testing.T) {
	setupBulkDB(t)
	db := database.GetDB()
	clients := []model.Client{
		{Email: "idle", ID: "ce8d33df-3a64-4f10-8f9b-91c3a8e0d001", Enable: true, ExpiryTime: -86400000},
		{Email: "active", ID: "ce8d33df-3a64-4f10-8f9b-91c3a8e0d002", Enable: true, ExpiryTime: -86400000},
	}
	inbound := mkInbound(t, 45001, model.VLESS, clientsSettings(t, clients))
	svc := &InboundService{}
	if err := svc.clientService.SyncInbound(db, inbound.Id, clients); err != nil {
		t.Fatal(err)
	}
	for _, client := range clients {
		if err := db.Create(&xray.ClientTraffic{Email: client.Email, InboundId: inbound.Id, Enable: true, ExpiryTime: client.ExpiryTime}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, traffic := range [][]*xray.ClientTraffic{
		{{Email: "idle"}},
		{{Email: "idle"}, {Email: "active", Up: 4, Down: 4}},
	} {
		if err := db.Transaction(func(tx *gorm.DB) error { return svc.addClientTraffic(tx, traffic) }); err != nil {
			t.Fatal(err)
		}
		var stored xray.ClientTraffic
		if err := db.Where("email = ?", "idle").First(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if stored.ExpiryTime != -86400000 || stored.LastOnline != 0 {
			t.Errorf("zero-byte stats started an unused client's duration: expiry=%d online=%d", stored.ExpiryTime, stored.LastOnline)
		}
	}
	var active xray.ClientTraffic
	if err := db.Where("email = ?", "active").First(&active).Error; err != nil {
		t.Fatal(err)
	}
	if active.ExpiryTime <= 0 || active.Up != 4 || active.Down != 4 {
		t.Fatalf("actual first use failed to start duration: %+v", active)
	}
}
