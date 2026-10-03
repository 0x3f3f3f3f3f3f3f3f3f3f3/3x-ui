package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

type authorityCaptureRecoveryFixture struct {
	config    *conf.ClientPolicyConfig
	operation model.ClientTrafficResetBatch
	capture   policyauthority.ResetOperationCapture
	account   policyauthority.Account
	grant     policyauthority.Grant
}

func assertAuthorityCaptureRecoveryFundedState(t *testing.T, journal *policyauthority.Journal, fixture authorityCaptureRecoveryFixture) {
	t.Helper()
	account, err := journal.Account(fixture.account.Seed.ClientID)
	if err != nil || account != fixture.account {
		t.Fatalf("metadata recovery changed full funded account: %v", err)
	}
	grant, err := journal.Grant(fixture.grant.GrantID)
	if err != nil || grant != fixture.grant {
		t.Fatalf("metadata recovery changed retained grant: %v", err)
	}
	capture, err := journal.LookupResetOperation(fixture.capture.RequestID)
	if err != nil || capture != fixture.capture {
		t.Fatalf("metadata recovery changed original capture: %v", err)
	}
}

func TestAuthorityResetCaptureRecoveryRejectsDamageAndStaleOwners(t *testing.T) {
	for _, fault := range []string{"immutable-sql", "invalid-envelope", "calendar-collision", "source", "cancelled", "replaced-pool", "projection-write"} {
		t.Run(fault, func(t *testing.T) {
			fixture := setupAuthorityCaptureRecoveryFixture(t)
			expected := database.GetDB()
			state, err := openAuthorityState(filepath.Join(filepath.Dir(fixture.config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			ctx := context.Background()
			source := fixture.config.InstanceID
			want := ErrClientPolicyLedger
			missing := false
			switch fault {
			case "immutable-sql":
				if err := expected.Table("client_traffic_reset_batches").Where("request_id = ?", fixture.operation.RequestID).UpdateColumn("targets_json", "[]").Error; err != nil {
					t.Fatal(err)
				}
			case "invalid-envelope":
				if err := state.Journal.CaptureResetOperation(policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: source, RequestID: "traffic-reset:invalid-service-envelope", Snapshot: `{"Schema":2}`}); err != nil {
					t.Fatal(err)
				}
			case "calendar-collision":
				original := model.ClientTrafficResetBatch{RequestID: "original-captured-calendar", Scope: "calendar:daily:0000000000000000", ScheduledAt: time.Now().UTC().UnixMilli(), TargetsJSON: "[]", InboundIDsJSON: "[]", ManagedIDsJSON: "[]"}
				raw, err := json.Marshal(authorityResetCaptureSnapshot{Schema: 1, Operation: original})
				if err != nil {
					t.Fatal(err)
				}
				if err := state.Journal.CaptureResetOperation(policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: source, RequestID: authorityResetRequestKey(original.RequestID), CalendarKey: authorityResetCalendarKey(original.Scope, original.ScheduledAt), Snapshot: string(raw)}); err != nil {
					t.Fatal(err)
				}
				collision := original
				collision.RequestID = "different-calendar-request"
				if err := expected.Create(&collision).Error; err != nil {
					t.Fatal(err)
				}
			case "source":
				source = "wrong-recovery-source"
			case "cancelled", "replaced-pool", "projection-write":
				if err := expected.Where("request_id = ?", fixture.operation.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
					t.Fatal(err)
				}
				missing = true
				if fault == "cancelled" {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx, want = cancelled, context.Canceled
				} else if fault == "replaced-pool" {
					dbtest.InitDB(t, filepath.Join(t.TempDir(), "replacement.db"))
					want = database.ErrDatabaseReplaced
				} else {
					want = errors.New("captured operation projection write failed")
					const callback = "test:fail-owned-capture-projection"
					if err := expected.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
						if tx.Statement.Table == "client_traffic_reset_batches" {
							tx.AddError(want)
						}
					}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = expected.Callback().Create().Remove(callback) })
				}
			}
			if err := recoverAuthorityResetCaptures(ctx, expected, state.Journal, source); !errors.Is(err, want) {
				t.Fatalf("unsafe capture recovery accepted (%s): %v", fault, err)
			}
			assertAuthorityCaptureRecoveryFundedState(t, state.Journal, fixture)
			if missing {
				var count int64
				if err := database.GetDB().Model(&model.ClientTrafficResetBatch{}).Where("request_id = ?", fixture.operation.RequestID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("failed capture projection reached SQL: %d/%v", count, err)
				}
			}
			if fault == "projection-write" {
				if err := expected.Callback().Create().Remove("test:fail-owned-capture-projection"); err != nil {
					t.Fatal(err)
				}
				if err := recoverAuthorityResetCaptures(context.Background(), expected, state.Journal, fixture.config.InstanceID); err != nil {
					t.Fatal(err)
				}
				var restored model.ClientTrafficResetBatch
				if err := expected.First(&restored, "request_id = ?", fixture.operation.RequestID).Error; err != nil || restored != fixture.operation {
					t.Fatalf("projection retry changed exact original operation: %v", err)
				}
				assertAuthorityCaptureRecoveryFundedState(t, state.Journal, fixture)
			}
		})
	}
}

func setupAuthorityCaptureRecoveryFixture(t *testing.T) authorityCaptureRecoveryFixture {
	t.Helper()
	config, _, _, before := authorityHistoryMigrationFixture(t)
	operation, err := captureClientTrafficResetBatch(context.Background(), "all", nil, "post-migration-owned-capture")
	if err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Journal.Close() })
	journal := state.Journal
	boot := policyauthority.NodeBoot{NodeID: "local", SourceID: config.InstanceID, BootID: "capture-recovery-boot"}
	if err := journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	grant, err := journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: journal.Identity(), NodeBoot: boot, ClientID: before.Seed.ClientID, WindowID: before.Policy.WindowID, PolicyVersion: before.Policy.Version}, RequestID: "capture-recovery-held-budget", ChallengeID: "capture-recovery-held-challenge", Capacity: 40, Upload: before.Policy.Upload, Download: before.Policy.Download, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	account, err := journal.Account(before.Seed.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := journal.LookupResetOperation(authorityResetRequestKey(operation.RequestID))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	return authorityCaptureRecoveryFixture{config: config, operation: operation, capture: capture, account: account, grant: grant}
}

func TestAuthorityResetCaptureRecoveryPreservesOriginalMetadataAndFundedState(t *testing.T) {
	for _, identity := range []string{"original-identity", "renamed-and-recreated-email", "deleted-and-recreated-email"} {
		t.Run(identity, func(t *testing.T) {
			fixture := setupAuthorityCaptureRecoveryFixture(t)
			db := database.GetDB()
			recreate := identity != "original-identity"
			if identity == "deleted-and-recreated-email" {
				if err := db.Where("stable_id = ?", fixture.account.Seed.ClientID).Delete(&model.ClientRecord{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Where("email = ?", "migration-owner").Delete(&xray.ClientTraffic{}).Error; err != nil {
					t.Fatal(err)
				}
			} else if recreate {
				if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", fixture.account.Seed.ClientID).Update("email", "renamed-captured-owner").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", "migration-owner").Update("email", "renamed-captured-owner").Error; err != nil {
					t.Fatal(err)
				}
			}
			if recreate {
				later := model.ClientRecord{Email: "migration-owner", Enable: true}
				if err := db.Create(&later).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&xray.ClientTraffic{Email: later.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Where("request_id = ?", fixture.operation.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := recoverAuthorityDesiredState(context.Background(), fixture.config); err != nil {
				t.Fatal(err)
			}
			var restored model.ClientTrafficResetBatch
			if err := db.First(&restored, "request_id = ?", fixture.operation.RequestID).Error; err != nil || restored != fixture.operation {
				t.Fatalf("cold recovery lost the exact original captured operation: %v", err)
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(fixture.config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			account, err := state.Journal.Account(fixture.account.Seed.ClientID)
			if err != nil || account != fixture.account {
				t.Fatalf("cold capture recovery changed the full funded account: %v", err)
			}
			grant, err := state.Journal.Grant(fixture.grant.GrantID)
			if err != nil || grant != fixture.grant {
				t.Fatalf("cold capture recovery changed retained capacity: %v", err)
			}
			capture, err := state.Journal.LookupResetOperation(fixture.capture.RequestID)
			if err != nil || capture != fixture.capture || currentXrayProcess() != nil {
				t.Fatalf("cold capture recovery changed its witness or opened the core: %v", err)
			}
			if recreate {
				var traffic xray.ClientTraffic
				if err := db.First(&traffic, "email = ?", "migration-owner").Error; err != nil || traffic.Up != 7 || traffic.Down != 11 {
					t.Fatalf("metadata recovery affected a recreated identity: up=%d down=%d err=%v", traffic.Up, traffic.Down, err)
				}
			}
			if identity == "deleted-and-recreated-email" {
				var count int64
				if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", fixture.account.Seed.ClientID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("metadata recovery recreated a deleted original identity: %d/%v", count, err)
				}
			}
		})
	}
}

// A first page containing unrelated storage captures must not hide a protected
// empty calendar on a later page or allow retry to select new eligible clients.
func TestAuthorityResetCaptureRecoveryPreservesEmptyCalendarAcrossPages(t *testing.T) {
	fixture := setupAuthorityCaptureRecoveryFixture(t)
	db := database.GetDB()
	if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", fixture.account.Seed.ClientID).Update("traffic_reset", "never").Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	original, err := captureScheduledTrafficReset(context.Background(), "daily", now)
	if err != nil || original.TargetsJSON != "[]" || original.InboundIDsJSON != "[]" {
		t.Fatalf("expected protected empty calendar: %+v/%v", original, err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(fixture.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 130; i++ {
		if err := state.Journal.CaptureResetOperation(policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: fixture.config.InstanceID, RequestID: fmt.Sprintf("opaque-recovery-fixture-%03d", i), Snapshot: `{"opaque":true}`}); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("request_id = ?", original.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	later := model.ClientRecord{Email: "later-daily-after-empty-capture", Enable: true, TrafficReset: "daily"}
	if err := db.Create(&later).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: later.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(context.Background(), fixture.config); err != nil {
		t.Fatal(err)
	}
	var restored model.ClientTrafficResetBatch
	if err := db.First(&restored, "request_id = ?", original.RequestID).Error; err != nil || restored != original {
		t.Fatalf("later recovery page lost original no-op calendar: %v", err)
	}
	retry, err := captureScheduledTrafficReset(context.Background(), "daily", now)
	if err != nil || retry != original {
		t.Fatalf("empty calendar retry selected a later identity: %v", err)
	}
	state, err = openAuthorityState(filepath.Join(filepath.Dir(fixture.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	assertAuthorityCaptureRecoveryFundedState(t, state.Journal, fixture)
	var traffic xray.ClientTraffic
	if err := db.First(&traffic, "email = ?", later.Email).Error; err != nil || traffic.Up != 7 || traffic.Down != 11 {
		t.Fatalf("no-op recovery changed later traffic: %d/%d/%v", traffic.Up, traffic.Down, err)
	}
}

// Removing the durable capture before SQL commit would lose the retry's
// original membership even though the caller never received a SQL success.
func TestAuthorityResetCaptureSQLCommitFailureRetainsOriginalSelection(t *testing.T) {
	fixture := setupAuthorityCaptureRecoveryFixture(t)
	db := database.GetDB()
	if db.Dialector.Name() == "sqlite" {
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			t.Fatal(err)
		}
		var enabled int
		if err := db.Raw("PRAGMA foreign_keys").Scan(&enabled).Error; err != nil || enabled != 1 {
			t.Fatalf("private SQLite connection lacks foreign-key enforcement: %d/%v", enabled, err)
		}
	}
	const callback = "test:owned-capture-commit-failure"
	installDeferredCommitFailure(t, db, "create", callback, "client_traffic_reset_batches", "capture_commit_parent", "capture_commit_child")
	const request = "capture-before-failed-sql-commit"
	operation, err := captureClientTrafficResetBatch(context.Background(), "all", nil, request)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("expected deferred commit failure after capture: %v", err)
	}
	var count int64
	if err := db.Model(&model.ClientTrafficResetBatch{}).Where("request_id = ?", request).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed SQL commit retained an operation: %d/%v", count, err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(fixture.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	capture, err := state.Journal.LookupResetOperation(authorityResetRequestKey(request))
	if err != nil {
		t.Fatalf("SQL commit failure lost the durable original selection: %v", err)
	}
	snapshot, err := decodeAuthorityResetCapture(capture, state.Journal, fixture.config.InstanceID)
	if err != nil || snapshot.Operation != operation || operation.RequestID != request {
		t.Fatalf("commit failure changed the exact original captured operation: %v", err)
	}
	assertAuthorityCaptureRecoveryFundedState(t, state.Journal, fixture)
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	later := model.ClientRecord{Email: "later-after-commit-failure", Enable: true}
	if err := db.Create(&later).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: later.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
		t.Fatal(err)
	}
	retry, err := captureClientTrafficResetBatch(context.Background(), "all", nil, request)
	if err != nil || retry != operation || strings.Contains(retry.TargetsJSON, later.StableID) {
		t.Fatalf("commit retry recaptured a later client or changed the original: %v", err)
	}
	state, err = openAuthorityState(filepath.Join(filepath.Dir(fixture.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	retained, err := state.Journal.LookupResetOperation(capture.RequestID)
	if err != nil || retained != capture {
		t.Fatalf("SQL retry replaced its durable capture: %v", err)
	}
	assertAuthorityCaptureRecoveryFundedState(t, state.Journal, fixture)
	var traffic xray.ClientTraffic
	if err := db.First(&traffic, "email = ?", later.Email).Error; err != nil || traffic.Up != 7 || traffic.Down != 11 {
		t.Fatalf("capture retry changed later traffic: %d/%d/%v", traffic.Up, traffic.Down, err)
	}
}
