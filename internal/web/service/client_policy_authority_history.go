package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

// Migration records are immutable evidence. Read and validate each bounded
// page before entering the serialized SQL projection; never rewrite the journal.
func recoverAuthorityMigrationHistory(ctx context.Context, expected *gorm.DB, journal *policyauthority.Journal) error {
	if ctx == nil || expected == nil || journal == nil {
		return ErrClientPolicyLedger
	}
	for _, kind := range []string{"reset-times", "reset-batches"} {
		after := ""
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			page, err := journal.MigrationPage(kind, "", after, 128)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			var times []model.ClientTrafficResetTime
			var operations []model.ClientTrafficResetBatch
			for _, record := range page {
				if kind == "reset-times" {
					var stamp model.ClientTrafficResetTime
					if json.Unmarshal(record.Value, &stamp) != nil || stamp.ClientID != record.Key || uuid.Validate(stamp.ClientID) != nil || stamp.EffectiveAt < 0 {
						return ErrClientPolicyLedger
					}
					times = append(times, stamp)
				} else {
					var operation model.ClientTrafficResetBatch
					if json.Unmarshal(record.Value, &operation) != nil || operation.RequestID != record.Key || !validAuthorityResetBatch(operation) {
						return ErrClientPolicyLedger
					}
					operations = append(operations, operation)
				}
			}
			if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
				for _, stamp := range times {
					if err := recordClientTrafficResetTimes(tx, []string{stamp.ClientID}, stamp.EffectiveAt); err != nil {
						return err
					}
				}
				for _, operation := range operations {
					if err := recoverAuthorityResetBatchTx(tx, operation); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
			after = page[len(page)-1].Key
		}
	}
	return nil
}

func validAuthorityResetBatch(operation model.ClientTrafficResetBatch) bool {
	if !validPolicySourceKey(operation.RequestID) || operation.ScheduledAt < 0 || operation.LastAttemptAt < 0 || operation.CreatedAt < 0 || operation.Affected < 0 {
		return false
	}
	if operation.ScheduledAt > 0 {
		parts := strings.Split(operation.Scope, ":")
		if len(parts) != 3 || parts[0] != "calendar" || parts[1] != "daily" && parts[1] != "weekly" && parts[1] != "monthly" || operation.SelectionHash != "" {
			return false
		}
		zone, err := hex.DecodeString(parts[2])
		if err != nil || len(zone) != 8 {
			return false
		}
	} else {
		validScope := operation.Scope == "all" || operation.Scope == "bulk"
		if strings.HasPrefix(operation.Scope, "inbound:") {
			id, err := strconv.Atoi(strings.TrimPrefix(operation.Scope, "inbound:"))
			validScope = err == nil && (id > 0 || id == -1)
		}
		digest, err := hex.DecodeString(operation.SelectionHash)
		if !validScope || err != nil || len(digest) != 32 {
			return false
		}
	}
	var targets []clientResetTarget
	var inboundIDs []int
	var managed []string
	if json.Unmarshal([]byte(operation.TargetsJSON), &targets) != nil || json.Unmarshal([]byte(operation.InboundIDsJSON), &inboundIDs) != nil || json.Unmarshal([]byte(operation.ManagedIDsJSON), &managed) != nil || len(targets) > 100000 || len(inboundIDs) > 100000 || len(managed) > 100000 || operation.Affected > len(targets) || operation.Applied && operation.Affected < len(managed) || !operation.Applied && (operation.Affected != 0 || len(managed) != 0) {
		return false
	}
	members := make(map[string]bool, len(targets))
	orphans := make(map[string]bool)
	for _, target := range targets {
		if target.Email == "" || !utf8.ValidString(target.Email) {
			return false
		}
		if target.ClientID == "" {
			if orphans[target.Email] {
				return false
			}
			orphans[target.Email] = true
		} else {
			if uuid.Validate(target.ClientID) != nil || members[target.ClientID] {
				return false
			}
			members[target.ClientID] = true
		}
	}
	for i, id := range inboundIDs {
		if id <= 0 || i > 0 && id <= inboundIDs[i-1] {
			return false
		}
	}
	for i, id := range managed {
		if !members[id] || i > 0 && id <= managed[i-1] {
			return false
		}
	}
	return true
}

func recoverAuthorityResetBatchTx(tx *gorm.DB, protected model.ClientTrafficResetBatch) error {
	var existing model.ClientTrafficResetBatch
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, "request_id = ?", protected.RequestID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if protected.ScheduledAt > 0 {
			var collision model.ClientTrafficResetBatch
			err := tx.First(&collision, "scope = ? AND scheduled_at = ?", protected.Scope, protected.ScheduledAt).Error
			if err == nil {
				return ErrClientPolicyLedger
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		// Map insertion preserves even an old zero CreatedAt; struct creation
		// would substitute today's auto-create timestamp for that original value.
		return tx.Table("client_traffic_reset_batches").Create(map[string]any{
			"request_id": protected.RequestID, "scope": protected.Scope, "scheduled_at": protected.ScheduledAt,
			"last_attempt_at": protected.LastAttemptAt, "inbound_ids_json": protected.InboundIDsJSON,
			"selection_hash": protected.SelectionHash, "targets_json": protected.TargetsJSON,
			"managed_ids_json": protected.ManagedIDsJSON, "applied": protected.Applied,
			"affected": protected.Affected, "created_at": protected.CreatedAt,
		}).Error
	}
	if err != nil {
		return err
	}
	if !validAuthorityResetBatch(existing) || existing.Scope != protected.Scope || existing.ScheduledAt != protected.ScheduledAt || existing.InboundIDsJSON != protected.InboundIDsJSON || existing.SelectionHash != protected.SelectionHash || existing.TargetsJSON != protected.TargetsJSON || existing.CreatedAt != protected.CreatedAt {
		return ErrClientPolicyLedger
	}
	if protected.Applied && existing.Applied && (existing.ManagedIDsJSON != protected.ManagedIDsJSON || existing.Affected != protected.Affected) {
		return ErrClientPolicyLedger
	}
	updates := map[string]any{}
	if existing.LastAttemptAt < protected.LastAttemptAt {
		updates["last_attempt_at"] = protected.LastAttemptAt
	}
	if protected.Applied && !existing.Applied {
		updates["applied"], updates["affected"], updates["managed_ids_json"] = true, protected.Affected, protected.ManagedIDsJSON
	}
	if len(updates) == 0 {
		return nil
	}
	result := tx.Model(&model.ClientTrafficResetBatch{}).Where("request_id = ?", protected.RequestID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrClientPolicyLedger
	}
	return nil
}
