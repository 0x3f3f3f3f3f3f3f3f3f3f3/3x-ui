package database

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func TestDumpSQLiteUsesOneSourceSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.db")
	writer, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`CREATE TABLE snapshot_first (value INTEGER); CREATE TABLE snapshot_second (value INTEGER); INSERT INTO snapshot_first VALUES (100); INSERT INTO snapshot_second VALUES (100)`); err != nil {
		t.Fatal(err)
	}
	changed := false
	var writeErr error
	driverName := fmt.Sprintf("snapshot_reader_%d", time.Now().UnixNano())
	sql.Register(driverName, &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		conn.RegisterAuthorizer(func(op int, table, column, database string) int {
			if op == sqlite3.SQLITE_READ && table == "snapshot_second" && !changed {
				changed = true
				_, writeErr = writer.Exec(`BEGIN; UPDATE snapshot_first SET value = 200; UPDATE snapshot_second SET value = 200; COMMIT`)
				if writeErr != nil {
					return sqlite3.SQLITE_DENY
				}
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	}})
	reader, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	dump, err := dumpSQLiteDatabase(reader)
	if err != nil {
		t.Fatalf("dump: %v (concurrent writer: %v)", err, writeErr)
	}
	if !changed || writeErr != nil {
		t.Fatalf("concurrent transaction did not commit between table reads: changed=%t err=%v", changed, writeErr)
	}
	dumpPath := filepath.Join(dir, "snapshot.dump")
	if err := os.WriteFile(dumpPath, dump, 0o600); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(dir, "restored.db")
	if err := RestoreSQLite(dumpPath, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite3", restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, table := range []string{"snapshot_first", "snapshot_second"} {
		var exported, current int
		if err := restored.QueryRow("SELECT value FROM " + table).Scan(&exported); err != nil {
			t.Fatal(err)
		}
		if err := writer.QueryRow("SELECT value FROM " + table).Scan(&current); err != nil {
			t.Fatal(err)
		}
		if exported != 100 || current != 200 {
			t.Errorf("mixed dump snapshot: %s exported=%d source=%d, want 100 and 200", table, exported, current)
		}
	}
}

func openSnapshotTestSource(t *testing.T, backend string) *gorm.DB {
	t.Helper()
	var dialector gorm.Dialector
	if backend == "sqlite" {
		dialector = sqlite.Open(filepath.Join(t.TempDir(), "source.db") + "?_journal_mode=WAL&_busy_timeout=10000")
	} else {
		dsn := strings.TrimSpace(os.Getenv("XUI_TEST_PG_DSN"))
		if dsn == "" {
			t.Skip("set XUI_TEST_PG_DSN for the isolated PostgreSQL snapshot test")
		}
		admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			t.Fatal(err)
		}
		schema := fmt.Sprintf("source_snapshot_%d", time.Now().UnixNano())
		if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
				t.Error(err)
			}
			closeGorm(admin)
		})
		if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			dsn = u.String()
		} else {
			dsn += " search_path=" + schema
		}
		dialector = postgres.Open(dsn)
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGorm(db) })
	for _, m := range migrationModels() {
		if err := db.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestCopyAllModelsUsesOneSourceSnapshot(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, boundary := range []string{"next-batch", "next-table"} {
			t.Run(backend+"/"+boundary, func(t *testing.T) {
				src := openSnapshotTestSource(t, backend)
				const clientID = "dcf5e7a1-1dad-4366-9999-000000000000"
				totals := make([]model.ClientPolicyTotal, 600)
				for i := range totals {
					totals[i] = model.ClientPolicyTotal{ClientID: fmt.Sprintf("dcf5e7a1-1dad-4366-9999-%012d", i), RawUpload: 100, BilledBytes: 100}
				}
				if err := src.CreateInBatches(totals, 200).Error; err != nil {
					t.Fatal(err)
				}
				if err := src.Create(&model.ClientPolicyReceipt{InstanceID: "source", ClientID: clientID, Epoch: 1, Sequence: 1, RawUpload: 100, BilledBytes: 100}).Error; err != nil {
					t.Fatal(err)
				}
				if err := src.Create(&model.ClientPolicyReset{ClientID: clientID, RequestID: "reset", InstanceID: "source", Epoch: 1, Sequence: 1, RawUpload: 100, BilledBytes: 100}).Error; err != nil {
					t.Fatal(err)
				}
				changed := false
				if err := src.Callback().Query().Before("gorm:query").Register("test:commit-between-snapshot-reads", func(q *gorm.DB) {
					trigger := q.Statement.Table == "client_policy_receipts"
					if boundary == "next-batch" {
						limit, ok := q.Statement.Clauses["LIMIT"].Expression.(clause.Limit)
						trigger = q.Statement.Table == "client_policy_totals" && ok && limit.Offset == 500
					}
					if changed || !trigger {
						return
					}
					changed = true
					// A different connection commits one coherent newer accounting state.
					if err := src.Transaction(func(tx *gorm.DB) error {
						for _, m := range []any{&model.ClientPolicyTotal{}, &model.ClientPolicyReceipt{}, &model.ClientPolicyReset{}} {
							stmt := &gorm.Statement{DB: tx}
							if err := stmt.Parse(m); err != nil {
								return err
							}
							if err := tx.Exec(`UPDATE "` + stmt.Schema.Table + `" SET raw_upload = 200, billed_bytes = 200`).Error; err != nil {
								return err
							}
						}
						return nil
					}); err != nil {
						q.AddError(err)
					}
				}); err != nil {
					t.Fatal(err)
				}
				dst, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "export.db")), &gorm.Config{Logger: logger.Discard})
				if err != nil {
					t.Fatal(err)
				}
				defer closeGorm(dst)
				if err := copyAllModels(src, dst); err != nil {
					t.Fatal(err)
				}
				if !changed {
					t.Fatal("concurrent transaction did not cross the export read boundary")
				}
				for _, m := range []any{&model.ClientPolicyTotal{}, &model.ClientPolicyReceipt{}, &model.ClientPolicyReset{}} {
					var newer int64
					if err := dst.Model(m).Where("raw_upload <> 100 OR billed_bytes <> 100").Count(&newer).Error; err != nil {
						t.Fatal(err)
					}
					if newer != 0 {
						t.Errorf("export mixed source snapshots: %T has %d newer rows", m, newer)
					}
				}
				var source model.ClientPolicyTotal
				if err := src.First(&source, "client_id = ?", clientID).Error; err != nil || source.BilledBytes != 200 {
					t.Fatalf("new source transaction did not commit: total=%+v err=%v", source, err)
				}
			})
		}
	}
}
