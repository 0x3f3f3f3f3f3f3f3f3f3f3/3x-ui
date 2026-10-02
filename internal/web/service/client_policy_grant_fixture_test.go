package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// openServiceFixtureAuthority performs the stopped-core migration used by the
// panel and opens its production owner. Tests must stop the owner before the
// child so every delivered byte has an exact, sealed grant report on restart.
func openServiceFixtureAuthority(t *testing.T, process *xray.Process, config *conf.ClientPolicyConfig) *managedAuthority {
	t.Helper()
	dir := filepath.Join(filepath.Dir(config.StateFile), "authority")
	if _, err := os.Lstat(filepath.Join(dir, "authority.json")); errors.Is(err, os.ErrNotExist) {
		owner, err := acquireDatabaseRestore()
		if err != nil {
			t.Fatal(err)
		}
		if err := owner.fenceDatabase(); err != nil {
			owner.release()
			t.Fatal(err)
		}
		state, err := migrateAuthorityWithOwner(context.Background(), owner, config.StateFile, config.InstanceID, dir)
		if err == nil {
			err = state.Journal.Close()
		}
		owner.release()
		if err != nil {
			t.Fatalf("migrate service fixture authority: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	authority, err := openManagedAuthority(process, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained service authority fixture: %s", filepath.Dir(config.StateFile))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := authority.Stop(ctx); err != nil && process.IsRunning() {
			t.Errorf("stop service fixture authority: %v", err)
		}
	})
	return authority
}

// Restricted grant fixtures exercise legacy receipt/cursor transactions without
// a background authority projection. They are not acceptance tests for ordinary
// polling under a retained production owner. The private RPC, journal issuance,
// capacity and monotonic lease bounds are still the production implementations.
func serviceFixtureGrantSeed(t *testing.T, ctx context.Context, api *xray.ClientPolicyAPI, client string) policyauthority.Seed {
	t.Helper()
	state, err := api.GetClient(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	p := state.Policy
	policy := clientpolicy.Policy{ClientID: client, Version: p.Version, Enabled: p.Enabled, QuotaBytes: p.QuotaBytes, Multiplier: p.MultiplierMicros, UploadRate: p.UploadBytesPerSecond, DownloadRate: p.DownloadBytesPerSecond, BurstBytes: p.BurstBytes}
	return policyauthority.Seed{ClientID: client, Policy: authorityPolicy(policy, "fixture:"+client), Usage: policyauthority.Usage{RawUpload: state.Usage.RawUpload, RawDownload: state.Usage.RawDownload, BilledBytes: state.Usage.BilledBytes, Remainder: state.Usage.Remainder}, WindowUsed: state.Usage.BilledBytes - p.QuotaBaselineBytes, WindowRemainder: state.Usage.Remainder}
}

func createServiceFixtureGrantJournal(t *testing.T, seeds []policyauthority.Seed) *policyauthority.Journal {
	t.Helper()
	dir, err := os.MkdirTemp("", "service-restricted-grant-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained restricted grant journal: %s", dir)
	journal, _, err := policyauthority.Create(filepath.Join(dir, "journal.db"), seeds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	return journal
}

func authorizeServiceFixtureGrant(t *testing.T, ctx context.Context, execution *authorityExecution, client, request string, capacity uint64) policyauthority.Grant {
	t.Helper()
	account, err := execution.journal.Account(client)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := execution.Authorize(ctx, authorityAllocation{ClientID: client, RequestID: request, Capacity: capacity, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: policyauthority.MaxLeaseDuration})
	if err != nil {
		t.Fatalf("issue restricted service fixture grant: %v", err)
	}
	installed, err := execution.api.GetAuthorityGrant(ctx, client, grant.GrantID)
	if err != nil || installed.Sealed || installed.Grant.BootId != execution.boot.BootID || installed.Grant.Capacity != capacity || installed.Grant.LeaseDurationMillis != uint64(policyauthority.MaxLeaseDuration.Milliseconds()) {
		t.Fatalf("restricted grant was not installed for this boot: %+v, %v", installed, err)
	}
	return grant
}

func assertServiceFixtureAuthorityUsage(t *testing.T, ctx context.Context, authority *managedAuthority, client string, upload, download, billed uint64) {
	t.Helper()
	if err := authority.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint real service fixture grant reports: %v", err)
	}
	account, err := authority.state.Journal.Account(client)
	if err != nil || account.Usage.RawUpload != upload || account.Usage.RawDownload != download || account.Usage.BilledBytes != billed {
		t.Fatalf("authority usage disagrees with independently observed payload: %+v, %v", account, err)
	}
}

func retainServiceFixtureAuthority(t *testing.T, process *xray.Process, authority *managedAuthority) {
	t.Helper()
	if err := retainManagedAuthority(process, authority); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := stopManagedAuthority(ctx, process); err != nil {
			t.Errorf("stop retained service fixture: %v", err)
		}
		if process.IsRunning() {
			if err := process.Stop(); err != nil {
				t.Errorf("stop retained fixture child: %v", err)
			}
		}
	})
}
