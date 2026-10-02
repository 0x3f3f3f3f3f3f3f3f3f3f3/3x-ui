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
