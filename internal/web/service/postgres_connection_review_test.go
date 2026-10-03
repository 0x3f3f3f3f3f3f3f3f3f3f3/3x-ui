package service

import (
	"strings"
	"testing"
)

func TestPostgresToolResolvedConnectionBoundaries(t *testing.T) {
	t.Setenv("PGUSER", "policy_test")
	for _, tc := range []struct{ name, dsn, key, want string }{
		{"literal-plus-password", "postgres://policy_test@localhost/db?password=synthetic+secret&sslmode=disable", "PGPASSWORD", "synthetic+secret"},
		{"literal-plus-option", "postgres://policy_test@localhost/db?application_name=backup+literal&sslmode=disable", "PGAPPNAME", "backup+literal"},
		{"empty-user-inherits-driver-principal", "host=localhost dbname=db user='' sslmode=disable", "PGUSER", "policy_test"},
		{"last-tls-alias-requires-encryption", "postgres://policy_test@localhost/db?sslmode=disable&ssl=true", "PGSSLMODE", "require"},
		{"last-explicit-tls-mode", "postgres://policy_test@localhost/db?ssl=true&sslmode=disable", "PGSSLMODE", "disable"},
		{"last-database-alias", "postgres://policy_test@localhost/first?dbname=second&database=third&dbname=fourth&sslmode=disable", "PGDATABASE", "fourth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for n := 0; n < 50; n++ {
				env, _, err := pgConnEnv(tc.dsn)
				if err != nil {
					t.Fatal(err)
				}
				found := ""
				for _, v := range env {
					if strings.HasPrefix(v, tc.key+"=") {
						found = strings.TrimPrefix(v, tc.key+"=")
					}
				}
				if found != tc.want {
					t.Fatalf("resolved %s differs from literal fixture", tc.key)
				}
			}
		})
	}
}

func TestPostgresToolRejectsInheritedDirectTLS(t *testing.T) {
	t.Setenv("PGSSLNEGOTIATION", "direct")
	if env, _, err := pgConnEnv("host=localhost dbname=db user=policy_test"); err == nil || env != nil {
		t.Fatal("PostgreSQL16 tools accepted unsupported effective direct TLS")
	}
}

func TestPostgresToolRejectsUnsupportedEffectiveProtocol(t *testing.T) {
	for _, key := range []string{"PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "3.2")
			if env, _, err := pgConnEnv("host=localhost dbname=db user=policy_test sslmode=disable"); err == nil || env != nil {
				t.Fatal("PostgreSQL16 tools accepted unsupported effective protocol requirement")
			}
		})
	}
}
