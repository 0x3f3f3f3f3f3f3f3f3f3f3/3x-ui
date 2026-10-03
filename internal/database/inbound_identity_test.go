package database

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func inboundIdentityDB(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("XUI_DB_TYPE") == "postgres" {
		cleanup, err := testpg.IsolatePackage("inbound_identity")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
	} else {
		t.Setenv("XUI_DB_TYPE", "sqlite")
	}
	if err := InitDB(filepath.Join(t.TempDir(), "identity.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	return GetDB()
}

func legacyInboundIdentityDB(t *testing.T, column bool) (string, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	var dialector gorm.Dialector = sqlite.Open(path)
	if os.Getenv("XUI_DB_TYPE") == "postgres" {
		cleanup, err := testpg.IsolatePackage("inbound_identity_legacy")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		dialector = postgres.Open(os.Getenv("XUI_DB_DSN"))
	} else {
		t.Setenv("XUI_DB_TYPE", "sqlite")
	}
	legacy, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	extra := ""
	if column {
		extra = ", stable_id TEXT"
	}
	if err := legacy.Exec("CREATE TABLE inbounds (id INTEGER PRIMARY KEY, user_id INTEGER, tag TEXT, port INTEGER, protocol TEXT, settings TEXT, up BIGINT, down BIGINT" + extra + ")").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = CloseDB()
		handle, _ := legacy.DB()
		_ = handle.Close()
	})
	return path, legacy
}

func TestInboundStableIdentityHistoricalMigration(t *testing.T) {
	for _, mode := range []string{"missing", "nullable", "partial"} {
		t.Run(mode, func(t *testing.T) {
			path, legacy := legacyInboundIdentityDB(t, mode != "missing")
			retained := map[int]string{}
			const settings = `{"clients":[],"credential":"preserved","opaque":9007199254740993}`
			if err := legacy.Transaction(func(tx *gorm.DB) error {
				for i := 1; i <= 1001; i++ {
					row := map[string]any{"id": i, "user_id": 1, "tag": fmt.Sprintf("legacy-%d", i), "port": 21000 + i, "protocol": "vless", "settings": settings, "up": 111 + i, "down": 222 + i}
					if mode == "partial" {
						if i%256 == 0 {
							retained[i] = uuid.NewString()
							row["stable_id"] = retained[i]
						} else if i%2 == 0 {
							row["stable_id"] = ""
						}
					}
					if err := tx.Table("inbounds").Create(row).Error; err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			handle, _ := legacy.DB()
			if err := handle.Close(); err != nil {
				t.Fatal(err)
			}
			if err := InitDB(path); err != nil {
				t.Fatal(err)
			}
			var rows []struct {
				model.Inbound
				PersistedID string `gorm:"column:stable_id"`
			}
			if err := GetDB().Table("inbounds").Select("*").Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1001 {
				t.Fatalf("migration changed row count: %d", len(rows))
			}
			seen := map[string]bool{}
			for _, row := range rows {
				parsed, err := uuid.Parse(row.PersistedID)
				if err != nil || parsed == uuid.Nil || parsed.String() != row.PersistedID || seen[row.PersistedID] {
					t.Fatalf("missing, invalid or reused migrated identity for row %d: %q", row.Id, row.PersistedID)
				}
				seen[row.PersistedID] = true
				if expected := retained[row.Id]; expected != "" && row.PersistedID != expected {
					t.Fatal("retained UUID changed")
				}
				if row.Settings != settings || row.Up != int64(111+row.Id) || row.Down != int64(222+row.Id) || row.Port != 21000+row.Id || row.Tag != fmt.Sprintf("legacy-%d", row.Id) {
					t.Fatalf("legacy business data changed for %d", row.Id)
				}
			}
			if err := InitDB(path); err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if storedInboundIdentity(t, GetDB(), row.Id) != row.PersistedID {
					t.Fatal("restart replaced identity")
				}
			}
		})
	}
}

func TestInboundStableIdentityRejectsCorruptMigration(t *testing.T) {
	canonical := "c1f175b1-b2bd-461a-bd35-650c2249a1fb"
	for _, identity := range []string{"credential", uuid.Nil.String(), strings.ToUpper(canonical), canonical} {
		t.Run(identity, func(t *testing.T) {
			path, legacy := legacyInboundIdentityDB(t, true)
			if err := legacy.Exec("INSERT INTO inbounds (id,tag,stable_id,settings,up,down) VALUES (1,'empty','',?,111,222),(2,'valid',?,?,333,444),(3,'bad',?,?,555,666)", `{"credential":"unchanged"}`, canonical, `{"credential":"unchanged"}`, identity, `{"credential":"unchanged"}`).Error; err != nil {
				t.Fatal(err)
			}
			if err := InitDB(path); err == nil {
				t.Fatal("initialization admitted invalid or duplicate retained identity")
			}
			var rows []struct {
				ID       int
				StableID string
				Settings string
				Up, Down int64
			}
			if err := legacy.Table("inbounds").Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 3 || rows[0].StableID != "" || rows[1].StableID != canonical || rows[2].StableID != identity {
				t.Fatalf("failed migration changed source identities: %+v", rows)
			}
			for i, row := range rows {
				if row.Settings != `{"credential":"unchanged"}` || row.Up != int64(111+222*i) || row.Down != int64(222+222*i) {
					t.Fatal("failed migration changed business data")
				}
			}
		})
	}
}

func storedInboundIdentity(t *testing.T, db *gorm.DB, id int) string {
	t.Helper()
	var ids []string
	if err := db.Table("inbounds").Where("id = ?", id).Pluck("stable_id", &ids).Error; err != nil {
		t.Fatalf("inbound has no persisted stable resource identity: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("identity rows: %v", ids)
	}
	parsed, err := uuid.Parse(ids[0])
	if err != nil || parsed == uuid.Nil || parsed.String() != ids[0] {
		t.Fatalf("invalid stable inbound UUID: %q", ids[0])
	}
	return ids[0]
}

func setInboundIdentityForContract(t *testing.T, inbound *model.Inbound, id string) {
	t.Helper()
	field := reflect.ValueOf(inbound).Elem().FieldByName("StableID")
	if !field.IsValid() || field.Kind() != reflect.String {
		t.Fatal("inbound stable identity is not available")
	}
	field.SetString(id)
}

func TestInboundStableIdentityLifecycle(t *testing.T) {
	db := inboundIdentityDB(t)
	inbound := model.Inbound{UserId: 1, Tag: "identity-original", Port: 19871, Protocol: model.VLESS, Enable: true, Up: 111, Down: 222, Settings: `{"clients":[],"credential":"preserved","opaque":9007199254740993}`}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	id := storedInboundIdentity(t, db, inbound.Id)
	setInboundIdentityForContract(t, &inbound, uuid.NewString())
	inbound.Tag, inbound.Port, inbound.Remark = "identity-renamed", 19872, "updated"
	if err := db.Save(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	var current model.Inbound
	if err := db.First(&current, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if storedInboundIdentity(t, db, inbound.Id) != id || current.Tag != inbound.Tag || current.Port != inbound.Port || current.Settings != inbound.Settings || current.Up != 111 || current.Down != 222 {
		t.Fatal("ordinary edit changed identity or original business data")
	}
	raw, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), id) {
		t.Fatal("private resource identity leaked into ordinary JSON")
	}
	duplicate := model.Inbound{Tag: "identity-duplicate", Port: 19873}
	setInboundIdentityForContract(t, &duplicate, id)
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate identity admitted")
	}
	invalid := model.Inbound{Tag: "identity-invalid", Port: 19874}
	setInboundIdentityForContract(t, &invalid, "credential-not-resource-id")
	if err := db.Create(&invalid).Error; err == nil {
		t.Fatal("invalid identity admitted")
	}
	if err := db.Delete(&current).Error; err != nil {
		t.Fatal(err)
	}
	reused := model.Inbound{Id: inbound.Id, UserId: 1, Tag: inbound.Tag, Port: inbound.Port, Protocol: inbound.Protocol, Settings: inbound.Settings}
	if err := db.Create(&reused).Error; err != nil {
		t.Fatal(err)
	}
	if storedInboundIdentity(t, db, reused.Id) == id {
		t.Fatal("new resource inherited deleted resource identity")
	}
}

func TestInboundStableIdentityDatabaseExport(t *testing.T) {
	for _, mode := range []string{"missing", "partial", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			src := inboundIdentityDB(t)
			rows := []model.Inbound{
				{Tag: "export-one", Port: 27201, Settings: `{"credential":"retained","opaque":9007199254740993}`, Up: 111, Down: 222},
				{Tag: "export-two", Port: 27202, Settings: `{"credential":"other"}`, Up: 333, Down: 444},
				{Tag: "export-three", Port: 27203, Settings: `{"credential":"third"}`, Up: 555, Down: 666},
			}
			if err := src.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			retained := storedInboundIdentity(t, src, rows[0].Id)
			if err := src.Migrator().DropIndex(&model.Inbound{}, "idx_inbounds_stable_id"); err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				if err := src.Migrator().DropColumn(&model.Inbound{}, "stable_id"); err != nil {
					t.Fatal(err)
				}
			} else if mode == "partial" {
				if err := src.Exec("UPDATE inbounds SET stable_id = NULL WHERE id = ?", rows[1].Id).Error; err != nil {
					t.Fatal(err)
				}
				if err := src.Exec("UPDATE inbounds SET stable_id = '' WHERE id = ?", rows[2].Id).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := src.Exec("UPDATE inbounds SET stable_id = 'credential-not-identity' WHERE id = ?", rows[0].Id).Error; err != nil {
					t.Fatal(err)
				}
			}
			dst, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "export.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { handle, _ := dst.DB(); _ = handle.Close() })
			err = copyAllModels(src, dst)
			if mode == "invalid" {
				if err == nil {
					t.Fatal("database export admitted invalid retained resource identity")
				}
				return
			}
			if err != nil {
				t.Fatalf("historical database cannot export independent inbound identities: %v", err)
			}
			seen := map[string]bool{}
			for _, before := range rows {
				identity := storedInboundIdentity(t, dst, before.Id)
				if seen[identity] || mode == "partial" && before.Id == rows[0].Id && identity != retained {
					t.Fatal("database export changed or reused a retained UUID")
				}
				seen[identity] = true
				var after model.Inbound
				if err := dst.First(&after, before.Id).Error; err != nil {
					t.Fatal(err)
				}
				if after.Settings != before.Settings || after.Up != before.Up || after.Down != before.Down || after.Tag != before.Tag || after.Port != before.Port {
					t.Fatal("database export changed exact business data")
				}
			}
			if mode == "partial" {
				var originals []string
				if err := src.Table("inbounds").Where("id = ?", rows[2].Id).Pluck("stable_id", &originals).Error; err != nil || len(originals) != 1 || originals[0] != "" {
					t.Fatal("read-only export backfilled the source")
				}
			}
		})
	}
}

func TestInboundStableIdentityRestoreStaging(t *testing.T) {
	active := inboundIdentityDB(t)
	sentinel := model.Inbound{Tag: "active-resource", Port: 27301}
	if err := active.Create(&sentinel).Error; err != nil {
		t.Fatal(err)
	}
	activeID := storedInboundIdentity(t, active, sentinel.Id)
	canonical := "c1f175b1-b2bd-461a-bd35-650c2249a1fb"
	for _, mode := range []string{"missing", "nullable", "partial", "invalid", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "staged.db")
			staged, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { handle, _ := staged.DB(); _ = handle.Close() })
			if err := staged.AutoMigrate(&model.User{}, &model.Setting{}); err != nil {
				t.Fatal(err)
			}
			column := ", stable_id TEXT"
			if mode == "missing" {
				column = ""
			}
			if err := staged.Exec("CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, port INTEGER, settings TEXT, up BIGINT, down BIGINT" + column + ")").Error; err != nil {
				t.Fatal(err)
			}
			const settings = `{"credential":"retained","opaque":9007199254740993}`
			for i := 1; i <= 3; i++ {
				row := map[string]any{"id": i, "tag": fmt.Sprintf("staged-%d", i), "port": 27310 + i, "settings": settings, "up": 111 * i, "down": 222 * i}
				if mode == "partial" || mode == "invalid" || mode == "duplicate" {
					row["stable_id"] = ""
					if i == 3 {
						row["stable_id"] = canonical
					}
					if i == 2 && mode == "invalid" {
						row["stable_id"] = "invalid-resource"
					} else if i == 2 && mode == "duplicate" {
						row["stable_id"] = canonical
					}
				}
				if err := staged.Table("inbounds").Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			err = PrepareSQLiteForMigration(path)
			if storedInboundIdentity(t, active, sentinel.Id) != activeID || GetDB() != active {
				t.Fatal("staging migration changed the active panel database")
			}
			if mode == "invalid" || mode == "duplicate" {
				if err == nil {
					t.Fatal("staging admitted corrupt retained identities")
				}
				var firstIDs []string
				if err := staged.Table("inbounds").Where("id = 1").Pluck("stable_id", &firstIDs).Error; err != nil || len(firstIDs) != 1 || firstIDs[0] != "" {
					t.Fatal("rejected staging did not roll back earlier identity writes")
				}
				return
			}
			if err != nil {
				t.Fatalf("valid historical backup refused before restore: %v", err)
			}
			identities := map[int]string{}
			seen := map[string]bool{}
			for i := 1; i <= 3; i++ {
				id := storedInboundIdentity(t, staged, i)
				if seen[id] || mode == "partial" && i == 3 && id != canonical {
					t.Fatal("staging changed or reused a retained identity")
				}
				identities[i], seen[id] = id, true
				var row model.Inbound
				if err := staged.First(&row, i).Error; err != nil || row.Settings != settings || row.Up != int64(111*i) || row.Down != int64(222*i) || row.Port != 27310+i || row.Tag != fmt.Sprintf("staged-%d", i) {
					t.Fatal("staging changed exact business data")
				}
			}
			if err := PrepareSQLiteForMigration(path); err != nil {
				t.Fatal(err)
			}
			for i, id := range identities {
				if storedInboundIdentity(t, staged, i) != id {
					t.Fatal("repeated staging changed identity")
				}
			}
		})
	}
}
