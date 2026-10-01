package testpg

import (
	"os"
	"strings"
	"testing"
)

func TestPostgresLongNamesRetainUniqueSchemaIdentity(t *testing.T) {
	if os.Getenv(dbTypeEnv) != "postgres" || os.Getenv(dbDSNEnv) == "" {
		t.Skip("requires actual isolated PostgreSQL")
	}
	name := "native_schema_identifier_boundary_" + strings.Repeat("native_", 20)
	first, err := IsolatePackage(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first)
	firstDSN := os.Getenv(dbDSNEnv)
	second, err := IsolatePackage(name)
	if err != nil {
		t.Fatalf("long test name discarded the unique schema suffix: %v", err)
	}
	t.Cleanup(second)
	secondDSN := os.Getenv(dbDSNEnv)
	if firstDSN == secondDSN {
		t.Fatal("two native test runs shared a schema identity")
	}
}
