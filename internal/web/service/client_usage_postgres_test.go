package service

import (
	"os"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
)

func TestManagedUsageLifecycle_Postgres(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"restrictions", TestManagedUsageTrafficTickPreservesIndependentRestrictionReasons},
		{"bulk-rollback", TestManagedUsageBulkResetRollsBackAllClients},
		{"expired-reset", TestManagedUsageResetKeepsExpiryRestriction},
		{"renewal", TestManagedUsageAutomaticRenewalKeepsManualDisable},
		{"quota-edit", TestManagedUsageQuotaIncreaseKeepsManualDisable},
		{"live-tcp", TestManagedUsageResetFencesLiveTCPOnceAcrossAttachments},
		{"batching", TestManagedUsageBulkResetKeepsLegacyBatching},
	} {
		t.Run(test.name, func(t *testing.T) {
			managedUsagePostgresSchema(t)
			test.run(t)
		})
	}
}

func managedUsagePostgresSchema(t *testing.T) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("XUI_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("set XUI_TEST_PG_DSN to an isolated PostgreSQL instance")
	}
	t.Setenv("XUI_DB_TYPE", "postgres")
	t.Setenv("XUI_DB_DSN", dsn)
	cleanup, err := testpg.IsolatePackage("managed_usage")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
}
