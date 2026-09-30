package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMigrationCLIReportsFailure(t *testing.T) {
	binary := os.Getenv("XUI_E2E_PANEL")
	if binary == "" {
		t.Skip("set XUI_E2E_PANEL to a normal panel binary")
	}
	for _, invalid := range []string{"settings-json", "client-null", "domain-number"} {
		t.Run(invalid, func(t *testing.T) {
			dir := t.TempDir()
			run := func() ([]byte, error) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "migrate")
				cmd.Env = append(os.Environ(), "XUI_DB_FOLDER="+dir, "XUI_DB_TYPE=sqlite", "XUI_DB_DSN=", "XUI_BIN_FOLDER="+filepath.Join(dir, "bin"), "XUI_LOG_FOLDER="+filepath.Join(dir, "log"))
				return cmd.CombinedOutput()
			}
			if out, err := run(); err != nil || !strings.Contains(string(out), "Migration done!") {
				t.Fatalf("fresh database migration: %v: %s", err, out)
			}
			db, err := sql.Open("sqlite3", filepath.Join(dir, "x-ui.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			settings, stream := `{"clients":[]}`, `{}`
			switch invalid {
			case "settings-json":
				settings = `{broken json`
			case "client-null":
				settings = `{"clients":[null]}`
			case "domain-number":
				stream = `{"security":"tls","tlsSettings":{"settings":{"domains":[{"domain":12}]}}}`
			}
			if _, err := db.Exec(`INSERT INTO inbounds(protocol,settings,stream_settings,enable,listen,port,tag) VALUES('vless',?,?,0,'127.0.0.1',0,'migration-fixture')`, settings, stream); err != nil {
				t.Fatal(err)
			}
			out, err := run()
			if err == nil || strings.Contains(string(out), "Migration done!") || !strings.Contains(string(out), "Database migration failed: MigrationRequirements failed:") {
				t.Fatalf("failed migration reported success or lost diagnostic: %v: %s", err, out)
			}
			var stored string
			if err := db.QueryRow(`SELECT settings FROM inbounds WHERE tag='migration-fixture'`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != settings {
				t.Fatal("failed migration changed inbound settings")
			}
		})
	}
}
