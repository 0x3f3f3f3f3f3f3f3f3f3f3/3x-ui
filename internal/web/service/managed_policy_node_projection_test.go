package service

import (
	"context"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func TestManagedPolicyNodeAccountProjectionUsesParentGuard(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	parent := model.ClientRecord{Email: "node-projection", Enable: true, TotalGB: 128}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	account, policy, err := prepareNodeClientPolicyContextForDatabase(ctx, db, parent.StableID, "node-a", "source-a")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := durableAuthorityFixture(t)
	state, err := initializeAuthorityState(dir, "projection-coordinator", authorityMigrationSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Journal.Close() })
	allowed := policyauthority.Direction{Unlimited: true}
	if err := state.Journal.AddAccount(policyauthority.Seed{ClientID: account.ClientID, Policy: policyauthority.Policy{WindowID: "node-window", Version: policy.Version, QuotaBytes: policy.QuotaBytes, Upload: allowed, Download: allowed}}); err != nil {
		t.Fatal(err)
	}
	if err := projectClientPolicyAuthority(ctx, db, state.Journal, account.ClientID); err != nil {
		t.Fatalf("canonical node account cannot project original authority: %v", err)
	}
	row, projected := authorityProjection(t, account.ClientID)
	if row.ClientID != account.ClientID || projected.Seed.ClientID != account.ClientID {
		t.Fatal("node account projection used parent/global balance")
	}
	if err := db.Create(&model.ClientPolicyTombstone{ClientID: parent.StableID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := projectClientPolicyAuthority(ctx, db, state.Journal, account.ClientID); err == nil {
		t.Fatal("deleted parent authorized live node account projection")
	}
}
