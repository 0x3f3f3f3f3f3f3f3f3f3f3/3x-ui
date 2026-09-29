package database

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestClientStableIdentityMigrationPostgres(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires a dedicated PostgreSQL test database")
	}
	for _, column := range []string{"", ", stable_id TEXT"} {
		t.Run(column, func(t *testing.T) {
			cleanup, err := testpg.IsolatePackage("client_identity")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			legacy, err := gorm.Open(postgres.Open(os.Getenv("XUI_DB_DSN")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			handle, err := legacy.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = handle.Close() })
			for _, sql := range []string{
				"CREATE TABLE clients (id BIGSERIAL PRIMARY KEY, email TEXT, uuid TEXT, total_gb BIGINT" + column + ")",
				"INSERT INTO clients (email,uuid,total_gb) VALUES ('one','credential',900),('two','other',1000)",
				"CREATE TABLE client_traffics (id BIGSERIAL PRIMARY KEY, email TEXT, up BIGINT, down BIGINT)",
				"INSERT INTO client_traffics (email,up,down) VALUES ('one',111,222)",
			} {
				if err := legacy.Exec(sql).Error; err != nil {
					t.Fatal(err)
				}
			}
			if column != "" {
				if err := legacy.Exec("UPDATE clients SET stable_id = ''").Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := InitDB(""); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = CloseDB() })
			var clients []model.ClientRecord
			if err := GetDB().Order("id").Find(&clients).Error; err != nil {
				t.Fatal(err)
			}
			if len(clients) != 2 || clients[0].StableID == clients[1].StableID {
				t.Fatal("identities not independent")
			}
			for _, client := range clients {
				if _, err := uuid.Parse(client.StableID); err != nil {
					t.Fatal(err)
				}
			}
			if clients[0].UUID != "credential" || clients[0].TotalGB != 900 {
				t.Fatal("migration changed credentials or quota")
			}
			var traffic xray.ClientTraffic
			if err := GetDB().First(&traffic).Error; err != nil {
				t.Fatal(err)
			}
			if traffic.Up != 111 || traffic.Down != 222 {
				t.Fatal("migration changed legacy usage")
			}
			if err := InitDB(""); err != nil {
				t.Fatal(err)
			}
			var again model.ClientRecord
			if err := GetDB().First(&again, clients[0].Id).Error; err != nil {
				t.Fatal(err)
			}
			if again.StableID != clients[0].StableID {
				t.Fatal("restart replaced stable identity")
			}
		})
	}
}
