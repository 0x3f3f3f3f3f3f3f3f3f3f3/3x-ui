package database

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestClientStableIdentityMigration(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	for _, nullable := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "nullable"}[nullable], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			column := ""
			if nullable {
				column = ", stable_id TEXT"
			}
			for _, sql := range []string{
				"CREATE TABLE clients (id INTEGER PRIMARY KEY, email TEXT, uuid TEXT, total_gb BIGINT" + column + ")",
				"INSERT INTO clients (id,email,uuid,total_gb) VALUES (1,'one','credential',900), (2,'two','another',1000)",
				"CREATE TABLE client_traffics (id INTEGER PRIMARY KEY, email TEXT, up BIGINT, down BIGINT)",
				"INSERT INTO client_traffics (id,email,up,down) VALUES (1,'one',111,222)",
			} {
				if err := legacy.Exec(sql).Error; err != nil {
					t.Fatal(err)
				}
			}
			if nullable {
				if err := legacy.Exec("UPDATE clients SET stable_id = ? WHERE id = 2", "c1f175b1-b2bd-461a-bd35-650c2249a1fb").Error; err != nil {
					t.Fatal(err)
				}
			}
			handle, _ := legacy.DB()
			if err := handle.Close(); err != nil {
				t.Fatal(err)
			}
			if err := InitDB(path); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = CloseDB() })
			var clients []model.ClientRecord
			if err := GetDB().Order("id").Find(&clients).Error; err != nil {
				t.Fatal(err)
			}
			if len(clients) != 2 {
				t.Fatalf("clients = %d", len(clients))
			}
			for _, c := range clients {
				if _, err := uuid.Parse(c.StableID); err != nil {
					t.Fatalf("invalid stable identity: %q", c.StableID)
				}
			}
			if clients[0].StableID == clients[1].StableID {
				t.Fatal("identity reused")
			}
			if nullable && clients[1].StableID != "c1f175b1-b2bd-461a-bd35-650c2249a1fb" {
				t.Fatal("existing identity replaced")
			}
			if clients[0].UUID != "credential" || clients[0].TotalGB != 900 {
				t.Fatal("legacy account changed")
			}
			var traffic xray.ClientTraffic
			if err := GetDB().First(&traffic).Error; err != nil {
				t.Fatal(err)
			}
			if traffic.Up != 111 || traffic.Down != 222 {
				t.Fatal("legacy usage changed")
			}
			if err := migrateClientStableIDs(); err != nil {
				t.Fatal(err)
			}
			var again model.ClientRecord
			if err := GetDB().First(&again, 1).Error; err != nil {
				t.Fatal(err)
			}
			if again.StableID != clients[0].StableID {
				t.Fatal("migration is not idempotent")
			}
		})
	}
}

func TestClientStableIdentityPartialMigrationWithEmptyIDs(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	path := filepath.Join(t.TempDir(), "partial.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"CREATE TABLE clients (id INTEGER PRIMARY KEY, email TEXT, stable_id TEXT)",
		"INSERT INTO clients (id,email,stable_id) VALUES (1,'one',''),(2,'two','')",
	} {
		if err := legacy.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	handle, _ := legacy.DB()
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	var clients []model.ClientRecord
	if err := GetDB().Order("id").Find(&clients).Error; err != nil {
		t.Fatal(err)
	}
	if len(clients) != 2 || clients[0].StableID == "" || clients[0].StableID == clients[1].StableID {
		t.Fatal("partial migration failed to allocate independent identities")
	}
}

func TestClientStableIdentityLifecycle(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	c := model.ClientRecord{Email: "original", UUID: "credential"}
	if err := GetDB().Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	id := c.StableID
	if _, err := uuid.Parse(id); err != nil {
		t.Fatal("new client has no stable identity")
	}
	c.Email, c.UUID, c.StableID = "renamed", "rotated", uuid.NewString()
	if err := GetDB().Save(&c).Error; err != nil {
		t.Fatal(err)
	}
	var stored model.ClientRecord
	if err := GetDB().First(&stored, c.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.StableID != id || stored.Email != "renamed" || stored.UUID != "rotated" {
		t.Fatal("rename/rotation changed stable identity")
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var restored model.ClientRecord
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.StableID != id {
		t.Fatal("JSON export lost identity")
	}
	duplicate := model.ClientRecord{Email: "duplicate", StableID: id}
	if err := GetDB().Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate identity accepted")
	}
	invalid := model.ClientRecord{Email: "invalid", StableID: "credential-as-identity"}
	if err := GetDB().Create(&invalid).Error; err == nil {
		t.Fatal("invalid identity accepted")
	}
	if err := GetDB().Delete(&stored).Error; err != nil {
		t.Fatal(err)
	}
	replacement := model.ClientRecord{Email: "renamed", UUID: "rotated"}
	if err := GetDB().Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	if replacement.StableID == id || replacement.StableID == "" {
		t.Fatal("deleted identity reused")
	}
}
