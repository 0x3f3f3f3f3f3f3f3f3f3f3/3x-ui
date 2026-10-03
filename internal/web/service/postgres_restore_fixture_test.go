package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func setupPrivatePostgresRestoreDatabase(t *testing.T) string {
	t.Helper()
	return setupPrivatePostgresRestoreDatabaseWithPrefix(t, "")
}

func setupPrivatePostgresRestoreDatabaseWithPrefix(t *testing.T, prefix string) string {
	t.Helper()
	dsn := os.Getenv("XUI_PG_RESTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set XUI_PG_RESTORE_TEST_DSN to a private fixture role with CREATE DATABASE")
	}
	for _, tool := range []string{"pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("required actual PostgreSQL tool missing: %s", tool)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("private PostgreSQL fixture connection configuration rejected")
	}
	defer admin.Close()
	name := "xui_restore_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if prefix != "" {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal("could not create private restore isolation peer")
		}
		name = prefix + name
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal("could not create isolated PostgreSQL restore database")
	}
	settings, err := postgresToolSettings(dsn)
	if err != nil {
		t.Fatal(err)
	}
	settings["dbname"] = name
	delete(settings, "search_path")
	var keys []string
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		value := strings.ReplaceAll(strings.ReplaceAll(settings[key], "\\", "\\\\"), "'", "\\'")
		parts = append(parts, key+"='"+value+"'")
	}
	t.Setenv("XUI_DB_TYPE", "postgres")
	t.Setenv("XUI_DB_DSN", strings.Join(parts, " "))
	dir, err := os.MkdirTemp("", "postgres-owned-restore-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained PostgreSQL restore database=%s artifacts=%s", name, dir)
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal("could not initialize private PostgreSQL restore database")
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	return dir
}

// A fabricated dump or a tool pointed at the shared fixture cannot contain
// both this random database identity and its literal sentinel row.
func TestPostgresToolBackupUsesActualPrivateDatabase(t *testing.T) {
	dir := setupPrivatePostgresRestoreDatabase(t)
	db := database.GetDB()
	if db.Dialector.Name() != "postgres" {
		t.Fatal("actual PostgreSQL backend required")
	}
	if err := db.Create(&model.Setting{Key: "actual-private-backup", Value: "literal-retained-sentinel"}).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := (&ServerService{}).exportPostgresDB()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "PGDMP") {
		t.Fatal("real PostgreSQL custom archive required")
	}
	archive := filepath.Join(dir, "actual-private-backup.dump")
	if err := os.WriteFile(archive, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listing, err := exec.CommandContext(ctx, "pg_restore", "--list", archive).Output()
	if err != nil || !strings.Contains(string(listing), "TABLE DATA public settings") {
		t.Fatal("actual archive did not list the private settings table")
	}
	data, err := exec.CommandContext(ctx, "pg_restore", "--data-only", "--table=settings", "--file=-", archive).Output()
	if err != nil || !strings.Contains(string(data), "actual-private-backup\tliteral-retained-sentinel") {
		t.Fatal("actual archive lost the private literal sentinel")
	}
}

func TestPostgresToolSessionOptionsPreserveValues(t *testing.T) {
	setupPrivatePostgresRestoreDatabase(t)
	t.Setenv("XUI_DB_DSN", config.GetDBDSN()+" xui.fixture='with trailing '")
	env, name, err := pgConnEnv(config.GetDBDSN())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "psql", "-X", "-At", "--dbname", name, "-c", "SELECT current_setting('xui.fixture')")
	cmd.Env = env
	result, err := cmd.Output()
	if err != nil || string(result) != "with trailing \n" {
		t.Fatalf("tool runtime option changed: %q/%v", result, err)
	}
}

func TestPostgresToolExplicitTimezoneOverridesInheritedEnvironment(t *testing.T) {
	setupPrivatePostgresRestoreDatabase(t)
	t.Setenv("PGTZ", "UTC")
	env, _, err := pgConnEnv(config.GetDBDSN() + " timezone='Asia/Tokyo'")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "psql", "-X", "-At", "-c", "SHOW timezone")
	cmd.Env = env
	result, err := cmd.Output()
	if err != nil || string(result) != "Asia/Tokyo\n" {
		t.Fatalf("explicit timezone overridden by inherited environment: %q/%v", result, err)
	}
}
