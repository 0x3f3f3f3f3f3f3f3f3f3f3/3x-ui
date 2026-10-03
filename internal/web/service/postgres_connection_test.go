package service

import (
	"strings"
	"testing"
)

// Dropping a socket query override or a keyword DSN connects the backup tools
// differently from the panel; duplicate inherited values make that ambiguous.
func TestPostgresToolConnectionSettings(t *testing.T) {
	t.Setenv("PGHOST", "inherited.invalid")
	t.Setenv("PGPORT", "5439")
	t.Setenv("PGUSER", "fixture-default")
	t.Setenv("PGPASSWORD", "inherited-fixture-only")
	t.Setenv("PGSSLMODE", "disable")
	cases := []struct {
		name, dsn, database string
		want                map[string]string
	}{
		{"keyword-socket", "host=/tmp/private-fixture-socket port=55491 user=fixture dbname=private_fixture sslmode=disable", "private_fixture", map[string]string{"PGHOST": "/tmp/private-fixture-socket", "PGPORT": "55491", "PGUSER": "fixture", "PGSSLMODE": "disable"}},
		{"uri-query-socket", "postgresql://fixture@unused.invalid:55491/private_fixture?host=%2Ftmp%2Fprivate-fixture-socket&sslmode=disable", "private_fixture", map[string]string{"PGHOST": "/tmp/private-fixture-socket", "PGPORT": "55491", "PGUSER": "fixture"}},
		{"quoted-keyword", `host=localhost port=55491 user=fixture dbname='private fixture' password='fixture \' quoted \\ value' sslmode=disable`, "private fixture", map[string]string{"PGPASSWORD": `fixture ' quoted \ value`, "PGHOST": "localhost"}},
		{"empty-password", "host=localhost port=55491 user=fixture dbname=private_fixture password='' sslmode=disable", "private_fixture", map[string]string{"PGPASSWORD": ""}},
		{"uri-password-override", "postgresql://fixture:old-fixture@localhost:55491/private_fixture?password=new-fixture&sslmode=disable", "private_fixture", map[string]string{"PGPASSWORD": "new-fixture"}},
		{"ipv6", "postgresql://fixture@[::1]:55491/private_fixture?sslmode=disable", "private_fixture", map[string]string{"PGHOST": "::1", "PGPORT": "55491"}},
		{"runtime-settings", "host=localhost port=55491 user=fixture dbname=private_fixture sslmode=disable search_path=private_schema statement_timeout=5000", "private_fixture", map[string]string{"PGOPTIONS": "-c search_path=private_schema -c statement_timeout=5000"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, database, err := pgConnEnv(tc.dsn)
			if err != nil || database != tc.database {
				t.Fatalf("tool connection not equivalent: database=%q error=%v", database, err)
			}
			values, counts := map[string]string{}, map[string]int{}
			for _, entry := range env {
				key, value, ok := strings.Cut(entry, "=")
				if ok {
					values[key], counts[key] = value, counts[key]+1
				}
			}
			for key, want := range tc.want {
				if values[key] != want || counts[key] != 1 {
					t.Fatalf("configured %s did not uniquely replace inherited value", key)
				}
			}
			if values["PGDATABASE"] != tc.database || counts["PGDATABASE"] != 1 {
				t.Fatal("tool database did not match explicit database")
			}
		})
	}
}

// A malformed connection must not leak its synthetic password into diagnostics
// or quietly select one host from an unsupported failover connection.
func TestPostgresToolConnectionRejectsUnsafeInput(t *testing.T) {
	const secret = "fixture-secret-do-not-log"
	cases := []string{
		"postgresql://fixture:" + secret + "@localhost/%zz",
		"host=localhost dbname=fixture password='" + secret,
		"host=localhost dbname=fixture password=" + secret + "\x00",
		"host='first.invalid,second.invalid' dbname=fixture password=" + secret,
		"postgresql://fixture:" + secret + "@first.invalid:5432,second.invalid:5433/fixture",
		"https://fixture:" + secret + "@localhost/fixture",
		"host=localhost user=fixture",
	}
	for i, dsn := range cases {
		env, _, err := pgConnEnv(dsn)
		if err == nil || len(env) != 0 || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe connection case%d was accepted or exposed its secret", i)
		}
	}
}
