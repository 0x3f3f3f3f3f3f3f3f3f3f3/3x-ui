package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/infra/conf"
)

func authorityHistoryMigrationFixture(t *testing.T) (*conf.ClientPolicyConfig, model.ClientTrafficResetBatch, int64, policyauthority.Account) {
	t.Helper()
	path, clientID, _ := authorityMigrationFixture(t)
	db := database.GetDB()
	ctx := context.Background()
	operation, err := captureClientTrafficResetBatch(ctx, "all", nil, "migration-original-all")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(operation.RequestID))
	if _, err := PrepareClientPolicyReset("migration-source", clientID, "batch:"+hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	members, err := json.Marshal([]string{clientID})
	if err != nil {
		t.Fatal(err)
	}
	operation.Applied, operation.Affected, operation.ManagedIDsJSON = true, 1, string(members)
	if err := db.Model(&operation).Select("applied", "affected", "managed_ids_json").Updates(&operation).Error; err != nil {
		t.Fatal(err)
	}
	// Preserve a historical clock-ahead stamp conservatively; recovery must
	// not substitute its own wall clock or lower the acknowledged boundary.
	protectedTime := time.Now().UTC().Add(time.Hour).UnixMilli()
	if err := recordClientTrafficResetTimes(db, []string{clientID}, protectedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLocalClientPolicyAuthority(ctx, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(path), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := state.Journal.Account(clientID)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	return &conf.ClientPolicyConfig{InstanceID: "migration-source", StateFile: path}, operation, protectedTime, before
}

func TestAuthorityMigrationHistoryRestoresOriginalBatchMembership(t *testing.T) {
	config, operation, protectedTime, before := authorityHistoryMigrationFixture(t)
	db, ctx, clientID := database.GetDB(), context.Background(), before.Seed.ClientID
	if err := db.Where("request_id = ?", operation.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client_id = ?", clientID).Delete(&model.ClientTrafficResetTime{}).Error; err != nil {
		t.Fatal(err)
	}
	var original model.ClientRecord
	if err := db.First(&original, "stable_id = ?", clientID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&original).Update("email", "renamed-after-original-selection").Error; err != nil {
		t.Fatal(err)
	}
	newClient := model.ClientRecord{Email: "migration-owner", Enable: true}
	if err := db.Create(&newClient).Error; err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(ctx, config); err != nil {
		t.Fatal(err)
	}
	retry, err := captureClientTrafficResetBatch(ctx, "all", nil, operation.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if retry != operation {
		t.Fatalf("old all-reset retry recaptured membership or acknowledgement: got %+v, want %+v", retry, operation)
	}
	var targets []clientResetTarget
	if err := json.Unmarshal([]byte(retry.TargetsJSON), &targets); err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.ClientID == newClient.StableID {
			t.Fatal("old request included a newly created canonical client")
		}
	}
	resolved, _, err := resolveClientResetTargets(db, retry.TargetsJSON, false)
	if err != nil || len(resolved) != 1 || resolved[0].StableID != clientID || resolved[0].Email != "renamed-after-original-selection" {
		t.Fatalf("original request followed a recreated email instead of its canonical member: %+v/%v", resolved, err)
	}
	var stamp model.ClientTrafficResetTime
	if err := db.First(&stamp, "client_id = ?", clientID).Error; err != nil || stamp.EffectiveAt != protectedTime {
		t.Fatalf("protected reset time disappeared or changed: %+v/%v", stamp, err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	after, err := state.Journal.Account(clientID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("metadata repair changed protected policy, window or budget: %+v/%+v/%v", before, after, err)
	}
}

func TestAuthorityMigrationHistoryRejectsConflictingOperation(t *testing.T) {
	for _, field := range []string{"scope", "scheduled_at", "selection_hash", "targets_json", "managed_ids_json", "created_at"} {
		t.Run(field, func(t *testing.T) {
			config, operation, _, before := authorityHistoryMigrationFixture(t)
			changes := map[string]any{"scope": "bulk", "scheduled_at": int64(1), "selection_hash": hex.EncodeToString(make([]byte, 32)), "managed_ids_json": "[]", "created_at": operation.CreatedAt + 1}
			targets := []clientResetTarget{{ClientID: before.Seed.ClientID, Email: "migration-owner"}, {ClientID: uuid.NewString(), Email: "unacknowledged-target"}}
			raw, err := json.Marshal(targets)
			if err != nil {
				t.Fatal(err)
			}
			changes["targets_json"] = string(raw)
			if err := database.GetDB().Table("client_traffic_reset_batches").Where("request_id = ?", operation.RequestID).UpdateColumn(field, changes[field]).Error; err != nil {
				t.Fatal(err)
			}
			if err := recoverAuthorityDesiredState(context.Background(), config); !errors.Is(err, ErrClientPolicyLedger) {
				t.Fatalf("conflicting restored %s was accepted: %v", field, err)
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			after, err := state.Journal.Account(before.Seed.ClientID)
			if err != nil || !reflect.DeepEqual(before, after) || currentXrayProcess() != nil {
				t.Fatalf("conflict changed protected accounting or activated a core: %+v/%+v/%v", before, after, err)
			}
		})
	}
}

func TestAuthorityMigrationHistoryRetriesProjectionFailure(t *testing.T) {
	config, operation, _, before := authorityHistoryMigrationFixture(t)
	db := database.GetDB()
	if err := db.Where("request_id = ?", operation.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("reset operation projection failure")
	const hook = "test:reset-history-projection"
	if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_traffic_reset_batches" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(hook) })
	if err := recoverAuthorityDesiredState(context.Background(), config); !errors.Is(err, injected) {
		t.Fatalf("missing operation was not projected or its failure was hidden: %v", err)
	}
	var count int64
	if err := db.Model(&model.ClientTrafficResetBatch{}).Where("request_id = ?", operation.RequestID).Count(&count).Error; err != nil || count != 0 || currentXrayProcess() != nil {
		t.Fatalf("failed projection left an operation or active core: %d/%v", count, err)
	}
	if err := db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	var restored model.ClientTrafficResetBatch
	if err := db.First(&restored, "request_id = ?", operation.RequestID).Error; err != nil || restored != operation {
		t.Fatalf("projection retry changed the original operation: %+v/%v", restored, err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	after, err := state.Journal.Account(before.Seed.ClientID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed/retried projection changed protected budget: %+v/%+v/%v", before, after, err)
	}
}

func TestAuthorityMigrationHistoryPromotesRestoredPendingAcknowledgement(t *testing.T) {
	config, operation, _, _ := authorityHistoryMigrationFixture(t)
	db := database.GetDB()
	// An older SQL snapshot can retain the captured selection while losing
	// the later acknowledgement. A newer retry-attempt time must still survive.
	newerAttempt := operation.LastAttemptAt + 500
	if err := db.Model(&model.ClientTrafficResetBatch{}).Where("request_id = ?", operation.RequestID).Updates(map[string]any{
		"applied": false, "affected": 0, "managed_ids_json": "[]", "last_attempt_at": newerAttempt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	var restored model.ClientTrafficResetBatch
	operation.LastAttemptAt = newerAttempt
	if err := db.First(&restored, "request_id = ?", operation.RequestID).Error; err != nil || restored != operation {
		t.Fatalf("recovery lost acknowledgement or regressed retry time: %+v/%v", restored, err)
	}
	if err := recoverAuthorityDesiredState(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	var retry model.ClientTrafficResetBatch
	if err := db.First(&retry, "request_id = ?", operation.RequestID).Error; err != nil || retry != restored {
		t.Fatalf("exact recovery retry changed operation history: %+v/%v", retry, err)
	}
}

func TestAuthorityMigrationHistoryRejectsCancelledAndReplacedDatabase(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "replaced"}[replaced], func(t *testing.T) {
			config, operation, _, before := authorityHistoryMigrationFixture(t)
			expected := database.GetDB()
			if err := expected.Where("request_id = ?", operation.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
				t.Fatal(err)
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantErr := error(context.Canceled)
			if replaced {
				// PostgreSQL reopens only this test's isolated schema. SQLite uses
				// a new private file; neither path overwrites the original fixture.
				dbtest.InitDB(t, filepath.Join(t.TempDir(), "replacement.db"))
				wantErr = database.ErrDatabaseReplaced
			} else {
				cancel()
			}
			if err := recoverAuthorityMigrationHistory(ctx, expected, state.Journal); !errors.Is(err, wantErr) {
				t.Fatalf("stale/cancelled history projection was accepted: %v", err)
			}
			var count int64
			if err := database.GetDB().Model(&model.ClientTrafficResetBatch{}).Where("request_id = ?", operation.RequestID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("old operation reached replacement/cancelled SQL: %d/%v", count, err)
			}
			after, err := state.Journal.Account(before.Seed.ClientID)
			if err != nil || !reflect.DeepEqual(before, after) || currentXrayProcess() != nil {
				t.Fatalf("failed history projection changed authority or opened traffic: %+v/%+v/%v", before, after, err)
			}
		})
	}
}

func TestAuthorityMigrationHistoryPreservesLaterAcknowledgement(t *testing.T) {
	path, clientID, _ := authorityMigrationFixture(t)
	db, ctx := database.GetDB(), context.Background()
	operation, err := captureClientTrafficResetBatch(ctx, "all", nil, "migration-pending-all")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLocalClientPolicyAuthority(ctx, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	operation.Applied, operation.Affected, operation.ManagedIDsJSON = true, 1, `["`+clientID+`"]`
	operation.LastAttemptAt = operation.CreatedAt + 500
	if err := db.Model(&operation).Select("applied", "affected", "managed_ids_json", "last_attempt_at").Updates(&operation).Error; err != nil {
		t.Fatal(err)
	}
	config := &conf.ClientPolicyConfig{InstanceID: "migration-source", StateFile: path}
	if err := recoverAuthorityDesiredState(ctx, config); err != nil {
		t.Fatal(err)
	}
	var recovered model.ClientTrafficResetBatch
	if err := db.First(&recovered, "request_id = ?", operation.RequestID).Error; err != nil || recovered != operation {
		t.Fatalf("protected pending state erased a later SQL acknowledgement: %+v/%v", recovered, err)
	}
}

func TestAuthorityMigrationHistoryRestoresCalendarAndOriginalZeroTimestamp(t *testing.T) {
	path, clientID, _ := authorityMigrationFixture(t)
	db, ctx := database.GetDB(), context.Background()
	if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", clientID).Update("traffic_reset", "daily").Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	operation, err := captureScheduledTrafficReset(ctx, "daily", now)
	if err != nil {
		t.Fatal(err)
	}
	// Old SQL exports may have a zero creation timestamp. Preserve that value
	// exactly instead of allowing GORM to substitute recovery's wall clock.
	if err := db.Table("client_traffic_reset_batches").Where("request_id = ?", operation.RequestID).UpdateColumn("created_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	operation.CreatedAt = 0
	if _, err := MigrateLocalClientPolicyAuthority(ctx, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("request_id = ?", operation.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	newClient := model.ClientRecord{Email: "calendar-new-client", Enable: true, TrafficReset: "daily"}
	if err := db.Create(&newClient).Error; err != nil {
		t.Fatal(err)
	}
	config := &conf.ClientPolicyConfig{InstanceID: "migration-source", StateFile: path}
	if err := recoverAuthorityDesiredState(ctx, config); err != nil {
		t.Fatal(err)
	}
	retry, err := captureScheduledTrafficReset(ctx, "daily", now)
	if err != nil || retry != operation {
		t.Fatalf("calendar retry changed original selection, request or timestamp: %+v/%v", retry, err)
	}
	resolved, _, err := resolveClientResetTargets(db, retry.TargetsJSON, false)
	if err != nil || len(resolved) != 1 || resolved[0].StableID != clientID {
		t.Fatalf("old calendar included a new client: %+v/%v", resolved, err)
	}
}

func TestAuthorityMigrationHistoryRestoresExistingHourlyCalendar(t *testing.T) {
	path, _, _ := authorityMigrationFixture(t)
	db, ctx := database.GetDB(), context.Background()
	now := time.Date(2026, 9, 30, 12, 35, 0, 0, time.UTC)
	operation, err := captureScheduledTrafficReset(ctx, "hourly", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLocalClientPolicyAuthority(ctx, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("request_id = ?", operation.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	config := &conf.ClientPolicyConfig{InstanceID: "migration-source", StateFile: path}
	if err := recoverAuthorityDesiredState(ctx, config); err != nil {
		t.Fatalf("existing hourly calendar blocked authority recovery: %v", err)
	}
	retry, err := captureScheduledTrafficReset(ctx, "hourly", now)
	if err != nil || retry != operation {
		t.Fatalf("hourly calendar was recaptured after recovery: %+v/%v", retry, err)
	}
}

func TestAuthorityHistoryRejectsRepeatedOlderCalendar(t *testing.T) {
	for _, source := range []string{"migration-stamp", "later-reset-missing-row", "later-reset-retained-row", "sql-time-already-ahead"} {
		t.Run(source, func(t *testing.T) {
			path, clientID, _ := authorityMigrationFixture(t)
			db, ctx := database.GetDB(), context.Background()
			oldAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
			calendarAt := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
			if err := db.Table("client_policy_resets").Where("client_id = ?", clientID).UpdateColumn("created_at", oldAt).Error; err != nil {
				t.Fatal(err)
			}
			migrationAt := oldAt
			if source == "migration-stamp" {
				migrationAt = calendarAt + 3600000
			}
			if err := db.Table("client_traffic_reset_times").Where("client_id = ?", clientID).UpdateColumn("effective_at", migrationAt).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := MigrateLocalClientPolicyAuthority(ctx, filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			state, err := openAuthorityState(filepath.Join(filepath.Dir(path), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			account, err := state.Journal.Account(clientID)
			if err != nil {
				t.Fatal(err)
			}
			config := conf.ClientPolicyConfig{InstanceID: "migration-source", StateFile: path}
			wantAt := migrationAt
			if source != "migration-stamp" {
				policy, err := PrepareClientPolicyReset(config.InstanceID, clientID, "later-reset-time-proof")
				if err != nil {
					t.Fatal(err)
				}
				// Use the production policy reconciler to commit the reset evidence;
				// no business core or test-only journal grant is involved.
				owner := &managedAuthority{config: config, db: db, state: state}
				if err := owner.reconcilePolicy(ctx, policy, account); err != nil {
					t.Fatal(err)
				}
				var reset model.ClientPolicyReset
				if err := db.First(&reset, "client_id = ? AND request_id = ?", clientID, "later-reset-time-proof").Error; err != nil {
					t.Fatal(err)
				}
				wantAt = reset.CreatedAt
				if wantAt <= calendarAt {
					t.Fatal("fixture reset did not cover the older calendar")
				}
				if source != "later-reset-retained-row" {
					if err := db.Where("client_id = ? AND request_id = ?", clientID, reset.RequestID).Delete(&model.ClientPolicyReset{}).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := state.Journal.Account(clientID)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			restoredAt := oldAt
			if source == "sql-time-already-ahead" {
				wantAt += 86400000
				restoredAt = wantAt
			}
			if err := db.Table("client_traffic_reset_times").Where("client_id = ?", clientID).UpdateColumn("effective_at", restoredAt).Error; err != nil {
				t.Fatal(err)
			}
			var client model.ClientRecord
			if err := db.First(&client, "stable_id = ?", clientID).Error; err != nil {
				t.Fatal(err)
			}
			operation := model.ClientTrafficResetBatch{ScheduledAt: calendarAt, TargetsJSON: `[{"clientId":"` + clientID + `","email":"migration-owner"}]`}
			eligible, err := scheduledResetEligibleClients(db, []model.ClientRecord{client}, operation, nil)
			if err != nil || (source != "sql-time-already-ahead" && len(eligible) != 1) {
				t.Fatalf("restored stale timestamp did not reproduce older-calendar eligibility: count %d/%v", len(eligible), err)
			}
			if err := recoverAuthorityDesiredState(ctx, &config); err != nil {
				t.Fatal(err)
			}
			eligible, err = scheduledResetEligibleClients(db, []model.ClientRecord{client}, operation, nil)
			if err != nil || len(eligible) != 0 {
				t.Fatalf("already covered older calendar became eligible after restore: count %d/%v", len(eligible), err)
			}
			var stamp model.ClientTrafficResetTime
			if err := db.First(&stamp, "client_id = ?", clientID).Error; err != nil || stamp.EffectiveAt != wantAt {
				t.Fatalf("recovery recaptured or regressed the original effective time: %+v, want %d/%v", stamp, wantAt, err)
			}
			state, err = openAuthorityState(filepath.Join(filepath.Dir(path), "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			after, err := state.Journal.Account(clientID)
			if err != nil || !reflect.DeepEqual(before, after) || currentXrayProcess() != nil {
				t.Fatalf("timestamp repair changed protected accounting/window or activated traffic: %+v/%+v/%v", before, after, err)
			}
		})
	}
}

func TestAuthorityHistoryRejectsNegativeResetTimestamp(t *testing.T) {
	config, _, _, before := authorityHistoryMigrationFixture(t)
	db := database.GetDB()
	var reset model.ClientPolicyReset
	if err := db.First(&reset, "client_id = ?", before.Seed.ClientID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", reset.Id).Delete(&model.ClientPolicyReset{}).Error; err != nil {
		t.Fatal(err)
	}
	reset.CreatedAt = -1
	err := runSerializedTx(func(tx *gorm.DB) error { return recoverAuthorityResetTx(tx, before, config.InstanceID, &reset) })
	if !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("invalid historical reset timestamp was projected: %v", err)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Where("client_id = ? AND request_id = ?", reset.ClientID, reset.RequestID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("invalid reset recovery committed metadata: %d/%v", count, err)
	}
}

func TestAuthorityHistoryPreservesOriginalZeroResetTimestamp(t *testing.T) {
	path, clientID, _ := authorityMigrationFixture(t)
	db, ctx := database.GetDB(), context.Background()
	if err := db.Table("client_policy_resets").Where("client_id = ?", clientID).UpdateColumn("created_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("client_traffic_reset_times").Where("client_id = ?", clientID).UpdateColumn("effective_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	var original model.ClientPolicyReset
	if err := db.First(&original, "client_id = ?", clientID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLocalClientPolicyAuthority(ctx, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client_id = ?", clientID).Delete(&model.ClientPolicyReset{}).Error; err != nil {
		t.Fatal(err)
	}
	config := &conf.ClientPolicyConfig{InstanceID: "migration-source", StateFile: path}
	if err := recoverAuthorityDesiredState(ctx, config); err != nil {
		t.Fatal(err)
	}
	var restored model.ClientPolicyReset
	if err := db.First(&restored, "client_id = ? AND request_id = ?", clientID, original.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	// SQL surrogate IDs are regenerated; the protected boundary and original
	// creation timestamp must otherwise remain exact through retries.
	restored.Id = original.Id
	if restored != original {
		t.Fatalf("recovery substituted today's clock for an original zero timestamp: %+v", restored)
	}
	if err := recoverAuthorityDesiredState(ctx, config); err != nil {
		t.Fatalf("zero timestamp recovery was not idempotent: %v", err)
	}
}

func TestAuthorityHistoryTimestampFailureRollsBackResetProjection(t *testing.T) {
	for _, missingReset := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing-reset", true: "missing-reset"}[missingReset], func(t *testing.T) {
			config, _, _, account := authorityHistoryMigrationFixture(t)
			db := database.GetDB()
			var reset model.ClientPolicyReset
			if err := db.First(&reset, "client_id = ?", account.Seed.ClientID).Error; err != nil {
				t.Fatal(err)
			}
			if missingReset {
				if err := db.Where("id = ?", reset.Id).Delete(&model.ClientPolicyReset{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Where("client_id = ?", reset.ClientID).Delete(&model.ClientTrafficResetTime{}).Error; err != nil {
				t.Fatal(err)
			}
			injected := errors.New("reset timestamp projection failure")
			const hook = "test:reset-time-projection"
			if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
				if tx.Statement.Table == "client_traffic_reset_times" {
					tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Create().Remove(hook) })
			project := func() error {
				return runSerializedTx(func(tx *gorm.DB) error { return recoverAuthorityResetTx(tx, account, config.InstanceID, &reset) })
			}
			if err := project(); !errors.Is(err, injected) {
				t.Fatalf("reset projection hid timestamp failure: %v", err)
			}
			var count int64
			wantCount := int64(1)
			if missingReset {
				wantCount = 0
			}
			if err := db.Model(&model.ClientPolicyReset{}).Where("client_id = ? AND request_id = ?", reset.ClientID, reset.RequestID).Count(&count).Error; err != nil || count != wantCount {
				t.Fatalf("timestamp failure committed a partial reset projection: %d/%v", count, err)
			}
			if err := db.Callback().Create().Remove(hook); err != nil {
				t.Fatal(err)
			}
			if err := project(); err != nil {
				t.Fatal(err)
			}
			var stamp model.ClientTrafficResetTime
			if err := db.First(&stamp, "client_id = ?", reset.ClientID).Error; err != nil || stamp.EffectiveAt != reset.CreatedAt {
				t.Fatalf("timestamp retry lost original reset time: %+v/%v", stamp, err)
			}
		})
	}
}
