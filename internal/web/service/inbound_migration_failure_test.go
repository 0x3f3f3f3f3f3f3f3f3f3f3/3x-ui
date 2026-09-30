package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestMigrationMalformedLegacyDomainRollsBack(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			setupConflictDB(t)
			db := database.GetDB()
			inbound := &model.Inbound{
				Tag: "invalid-domain", Port: 30204, Protocol: model.VLESS,
				Settings:       `{"clients":[{"id":"migration-client","email":"rollback@example.test"}]}`,
				StreamSettings: `{"security":"tls","tlsSettings":{"settings":{"domains":[{"domain":12}]}}}`,
			}
			if err := db.Create(inbound).Error; err != nil {
				t.Fatal(err)
			}
			var migrationErr error
			var panicked any
			func() {
				defer func() { panicked = recover() }()
				migrationErr = (&InboundService{}).MigrationRequirements()
			}()
			var count int64
			if err := db.Model(&xray.ClientTraffic{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("migration committed client traffic before malformed domain failure: %d rows", count)
			}
			var got model.Inbound
			if err := db.First(&got, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			if got.Settings != inbound.Settings || got.StreamSettings != inbound.StreamSettings {
				t.Error("failed migration changed stored inbound")
			}
			if migrationErr == nil || panicked != nil {
				t.Errorf("malformed legacy domain must return an error, got error=%v panic=%v", migrationErr, panicked)
			}
		})
	}
}

func TestMigrateDBStopsAtFailedPhase(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, phase := range []string{"requirements", "orphan-cleanup", "vision-load", "vision-read", "vision-write"} {
			t.Run(fmt.Sprintf("postgres=%t/%s", postgres, phase), func(t *testing.T) {
				if postgres {
					managedUsagePostgresSchema(t)
				}
				setupConflictDB(t)
				db := database.GetDB()
				svc := &InboundService{}
				sibling := &model.Inbound{
					Tag: "sibling", Port: 30205, Protocol: model.VLESS,
					Settings:       `{"clients":[{"id":"migration-client","email":"vision@example.test","flow":"xtls-rprx-vision"}]}`,
					StreamSettings: `{"network":"tcp","security":"reality"}`,
				}
				target := &model.Inbound{
					Tag: "target", Port: 30206, Protocol: model.VLESS,
					Settings:       `{"clients":[{"id":"migration-client","email":"vision@example.test"}]}`,
					StreamSettings: sibling.StreamSettings,
				}
				for _, inbound := range []*model.Inbound{sibling, target} {
					if err := db.Create(inbound).Error; err != nil {
						t.Fatal(err)
					}
					clients, err := svc.GetClients(inbound)
					if err != nil {
						t.Fatal(err)
					}
					if err := svc.clientService.SyncInbound(db, inbound.Id, clients); err != nil {
						t.Fatal(err)
					}
				}
				orphan := &xray.ClientTraffic{InboundId: target.Id, Email: "orphan@example.test", Up: 31, Down: 47}
				if err := db.Create(orphan).Error; err != nil {
					t.Fatal(err)
				}
				injected := errors.New("injected " + phase)
				switch phase {
				case "requirements":
					if err := db.Model(target).Update("settings", `{"clients":[null]}`).Error; err != nil {
						t.Fatal(err)
					}
				case "orphan-cleanup":
					if err := db.Callback().Raw().Before("gorm:raw").Register("test:orphan-failure", func(tx *gorm.DB) {
						if strings.HasPrefix(tx.Statement.SQL.String(), "DELETE FROM client_traffics WHERE email") {
							tx.AddError(injected)
						}
					}); err != nil {
						t.Fatal(err)
					}
				case "vision-load":
					if err := db.Callback().Query().After("gorm:query").Register("test:vision-load", func(tx *gorm.DB) {
						if tx.Statement.Table == "inbounds" && strings.Contains(tx.Statement.SQL.String(), "protocol =") {
							tx.AddError(injected)
						}
					}); err != nil {
						t.Fatal(err)
					}
				case "vision-read":
					if err := db.Callback().Row().Before("gorm:row").Register("test:vision-read", func(tx *gorm.DB) {
						if tx.Statement.Table == "client_inbounds" {
							tx.AddError(injected)
						}
					}); err != nil {
						t.Fatal(err)
					}
				case "vision-write":
					if err := db.Callback().Update().Before("gorm:update").Register("test:vision-write", func(tx *gorm.DB) {
						if tx.Statement.Table == "inbounds" {
							tx.AddError(injected)
						}
					}); err != nil {
						t.Fatal(err)
					}
				}
				err := svc.MigrateDB()
				if err == nil || (phase != "requirements" && !errors.Is(err, injected)) {
					t.Fatalf("lost migration error: %v", err)
				}
				if strings.HasPrefix(phase, "vision-") && !strings.Contains(err.Error(), "MigrationRestoreVisionFlow") {
					t.Fatalf("failure happened outside the expected Vision phase: %v", err)
				}
				var got model.Inbound
				if err := db.First(&got, target.Id).Error; err != nil {
					t.Fatal(err)
				}
				if strings.Contains(got.Settings, visionFlow) {
					t.Fatal("failed migration committed Vision repair")
				}
				if phase == "requirements" || phase == "orphan-cleanup" {
					var traffic xray.ClientTraffic
					if err := db.Where("email = ?", orphan.Email).First(&traffic).Error; err != nil {
						t.Fatal(err)
					}
					if traffic.Up != 31 || traffic.Down != 47 {
						t.Fatal("failed migration changed traffic counters")
					}
				}
			})
		}
	}
}

func TestMigrationRequirementsRollsBackOnPanic(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			setupConflictDB(t)
			db := database.GetDB()
			inbound := &model.Inbound{
				Tag: "panic-fixture", Port: 30207, Protocol: model.VLESS,
				Settings: `{"clients":[{"id":"panic-client","email":"panic@example.test"}]}`, StreamSettings: `{}`,
			}
			if err := db.Create(inbound).Error; err != nil {
				t.Fatal(err)
			}
			const message = "injected panic after traffic insert"
			if err := db.Callback().Create().After("gorm:create").Register("test:migration-panic", func(tx *gorm.DB) {
				if tx.Statement.Table == "client_traffics" && tx.Error == nil {
					panic(message)
				}
			}); err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if got := recover(); got != message {
						t.Errorf("panic=%v", got)
					}
				}()
				_ = (&InboundService{}).MigrationRequirements()
				t.Error("injected callback did not panic")
			}()
			var count int64
			if err := db.Model(&xray.ClientTraffic{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("panic committed %d rows", count)
			}
		})
	}
}
