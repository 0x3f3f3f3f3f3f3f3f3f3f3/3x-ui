package service

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

// Disposable SQL restored before real local consumption must not establish a
// second original issuer with an empty lifetime balance.
func TestManagedPolicyOriginalLocalHistoryRefusesSQLRollbackSeed(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain-%v", uncertain), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			svc, tunnel, parent, _ := setupManagedActivationServiceWithUsage(t, 0, 0)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = flow.Close() })
			managedActivationEcho(t, flow, "warm")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			process := currentXrayProcess()
			if uncertain {
				if err := process.Stop(); err != nil {
					t.Fatal(err)
				}
				if err := closeStoppedManagedAuthority(ctx, process); err != nil {
					t.Fatal(err)
				}
			} else if err := svc.StopXray(); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(config.GetDBFolderPath(), "client-policy", "authority")
			original, err := openAuthorityState(dir)
			if err != nil {
				t.Fatal(err)
			}
			before, err := original.Journal.Account(parent.StableID)
			if err != nil || before.Usage.RawUpload != 4 || before.Usage.RawDownload != 4 || before.Usage.BilledBytes != 16 || uncertain && before.HeldCapacity == 0 {
				t.Fatalf("actual local history missing: %+v/%v", before, err)
			}
			if err := original.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			db := database.GetDB()
			for _, table := range []string{"client_policy_receipts", "client_policy_totals", "client_policy_authority_projections", "client_policy_resets"} {
				if err := db.Table(table).Where("client_id = ?", parent.StableID).Delete(nil).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Table("client_traffics").Where("email = ?", parent.Email).Updates(map[string]any{"up": 0, "down": 0}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.ClientPolicySource{}).Where("node_key = ?", "local").Updates(map[string]any{"epoch": 0, "sequence": 0}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(parent).Updates(map[string]any{"policy_scope": "global", "desired_policy_version": 0, "policy_fingerprint": ""}).Error; err != nil {
				t.Fatal(err)
			}
			c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close(ctx) })
			t.Logf("original rollback admission backend: %s", db.Dialector.Name())
			if _, _, err := c.PrepareAccount(ctx, parent.StableID, managedAuthorityMember{NodeID: "fresh-remote", SourceID: "fresh-remote-source"}); err == nil {
				t.Fatal("SQL rollback minted a fresh managed account over retained local execution")
			}
			if _, err := c.state.Journal.LookupAccount(parent.StableID); err != policyauthority.ErrNotFound {
				t.Fatalf("refused admission created replacement credit: %v", err)
			}
			original, err = openAuthorityState(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer original.Journal.Close()
			after, err := original.Journal.Account(parent.StableID)
			if err != nil || after != before {
				t.Fatalf("refusal changed original consumption/held budget: %+v/%v", after, err)
			}
		})
	}
}
