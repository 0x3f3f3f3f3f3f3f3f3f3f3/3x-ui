package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type clientResetTarget struct {
	ClientID     string `json:"clientId"`
	Email        string `json:"email"`
	EnableLegacy bool   `json:"enableLegacy,omitempty"`
}

func (s *ClientService) BulkResetTrafficWithRequest(ctx context.Context, inboundSvc *InboundService, emails []string, requestID string) (int, error) {
	affected, needRestart, err := s.resetTrafficBatch(ctx, inboundSvc, "bulk", emails, requestID)
	if needRestart {
		(&XrayService{}).SetToNeedRestart()
	}
	return affected, err
}

func (s *ClientService) ResetAllTrafficsWithRequest(ctx context.Context, requestID string) (bool, error) {
	_, needRestart, err := s.resetTrafficBatch(ctx, &InboundService{}, "all", nil, requestID)
	return needRestart, err
}

func (s *ClientService) ResetAllClientTrafficsWithRequest(ctx context.Context, inboundSvc *InboundService, id int, requestID string) error {
	_, needRestart, err := s.resetTrafficBatch(ctx, inboundSvc, "inbound:"+strconv.Itoa(id), nil, requestID)
	if needRestart {
		(&XrayService{}).SetToNeedRestart()
	}
	return err
}

func captureClientTrafficResetBatch(ctx context.Context, scope string, emails []string, requestID string) (model.ClientTrafficResetBatch, error) {
	var operation model.ClientTrafficResetBatch
	if requestID == "" {
		requestID = uuid.NewString()
	}
	if !validPolicySourceKey(requestID) {
		return operation, ErrClientPolicyLedger
	}
	emails = trimmedUniqueEmails(emails)
	slices.Sort(emails)
	selection, err := json.Marshal(emails)
	if err != nil {
		return operation, err
	}
	hash := sha256.Sum256(selection)
	fingerprint := hex.EncodeToString(hash[:])
	err = runAuthorityResetCapture(ctx, authorityResetRequestKey(requestID), "", &operation, func(tx *gorm.DB) error {
		return selectClientTrafficResetBatchTx(tx.WithContext(ctx), &operation, scope, emails, requestID, fingerprint)
	}, func(original model.ClientTrafficResetBatch) error {
		return validateResetBatchSelection(original, scope, fingerprint)
	})
	return operation, err
}

// SQL selection is separate from source-owned admission; public callers enter
// through runAuthorityResetCapture before invoking this transaction helper.
func selectClientTrafficResetBatchTx(tx *gorm.DB, operation *model.ClientTrafficResetBatch, scope string, emails []string, requestID, fingerprint string) error {
	err := tx.First(operation, "request_id = ?", requestID).Error
	if err == nil {
		return validateResetBatchSelection(*operation, scope, fingerprint)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	selected := emails
	if scope == "all" || scope == "inbound:-1" {
		if err := tx.Raw("SELECT email FROM clients UNION SELECT email FROM client_traffics LIMIT 100001").Scan(&selected).Error; err != nil {
			return err
		}
	} else if strings.HasPrefix(scope, "inbound:") {
		id, err := strconv.Atoi(strings.TrimPrefix(scope, "inbound:"))
		if err != nil || id <= 0 {
			return errors.New("invalid reset inbound")
		}
		if err := tx.Table("client_inbounds ci").Select("DISTINCT c.email").Joins("JOIN clients c ON c.id = ci.client_id").Where("ci.inbound_id = ?", id).Limit(100001).Pluck("c.email", &selected).Error; err != nil {
			return err
		}
	}
	if len(selected) > 100000 {
		return errors.New("reset selection exceeds 100000 clients")
	}
	records, err := clientRecordsByEmail(tx, selected)
	if err != nil {
		return err
	}
	traffic := make(map[string]bool)
	for _, batch := range chunkStrings(selected, sqlInChunk) {
		var found []string
		if err := tx.Model(&xray.ClientTraffic{}).Where("email IN ?", batch).Pluck("email", &found).Error; err != nil {
			return err
		}
		for _, email := range found {
			traffic[email] = true
		}
	}
	targets := make([]clientResetTarget, 0, len(selected))
	for _, email := range selected {
		if record := records[email]; record != nil {
			targets = append(targets, clientResetTarget{ClientID: record.StableID, Email: email})
		} else if traffic[email] {
			targets = append(targets, clientResetTarget{Email: email})
		}
	}
	raw, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	*operation = model.ClientTrafficResetBatch{RequestID: requestID, Scope: scope, SelectionHash: fingerprint, TargetsJSON: string(raw), ManagedIDsJSON: "[]"}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(operation).Error; err != nil {
		return err
	}
	if err := tx.First(operation, "request_id = ?", requestID).Error; err != nil {
		return err
	}
	return validateResetBatchSelection(*operation, scope, fingerprint)
}

func validateResetBatchSelection(operation model.ClientTrafficResetBatch, scope, fingerprint string) error {
	if operation.Scope != scope || operation.SelectionHash != fingerprint {
		return errors.New("requestId belongs to a different reset selection")
	}
	return nil
}

func resolveClientResetTargets(tx *gorm.DB, raw string, locked bool) ([]model.ClientRecord, []string, error) {
	var targets []clientResetTarget
	if err := json.Unmarshal([]byte(raw), &targets); err != nil || len(targets) > 100000 {
		return nil, nil, ErrClientPolicyLedger
	}
	var ids, orphanEmails []string
	for _, target := range targets {
		if target.ClientID != "" {
			ids = append(ids, target.ClientID)
		} else {
			orphanEmails = append(orphanEmails, target.Email)
		}
	}
	slices.Sort(ids)
	if locked {
		tx = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})
	}
	var records []model.ClientRecord
	for _, batch := range chunkStrings(ids, 1000) {
		var found []model.ClientRecord
		if err := tx.Where("stable_id IN ?", batch).Order("stable_id").Find(&found).Error; err != nil {
			return nil, nil, err
		}
		records = append(records, found...)
	}
	owners, err := clientRecordsByEmail(tx, orphanEmails)
	if err != nil {
		return nil, nil, err
	}
	var orphans []string
	for _, email := range orphanEmails {
		if owners[email] == nil {
			orphans = append(orphans, email)
		}
	}
	return records, orphans, nil
}

func managedClientResetIDs(tx *gorm.DB, records []model.ClientRecord) ([]string, error) {
	ids := make([]string, len(records))
	for i, record := range records {
		ids[i] = record.StableID
	}
	prepared := make(map[string]bool)
	for _, batch := range chunkStrings(ids, 1000) {
		var found []string
		if err := tx.Raw(`SELECT client_id FROM client_policy_receipts WHERE client_id IN ?
			UNION SELECT client_id FROM client_policy_totals WHERE client_id IN ?`, batch, batch).Scan(&found).Error; err != nil {
			return nil, err
		}
		for _, id := range found {
			prepared[id] = true
		}
	}
	if process := currentXrayProcess(); process != nil {
		if config := process.GetConfig(); config != nil && len(config.ClientPolicy) > 0 {
			var policyConfig conf.ClientPolicyConfig
			if err := json.Unmarshal(config.ClientPolicy, &policyConfig); err != nil {
				return nil, err
			}
			for _, policy := range policyConfig.Policies {
				prepared[policy.ClientID] = true
			}
		}
	}
	var managed []string
	for _, id := range ids {
		if prepared[id] {
			managed = append(managed, id)
		}
	}
	slices.Sort(managed)
	return managed, nil
}

func resetBatchManagedIDs(tx *gorm.DB, operation model.ClientTrafficResetBatch, records []model.ClientRecord) ([]string, error) {
	if !operation.Applied {
		return managedClientResetIDs(tx, records)
	}
	var ids []string
	if err := json.Unmarshal([]byte(operation.ManagedIDsJSON), &ids); err != nil {
		return nil, ErrClientPolicyLedger
	}
	existing := make(map[string]bool, len(records))
	for _, record := range records {
		existing[record.StableID] = true
	}
	ids = slices.DeleteFunc(ids, func(id string) bool { return !existing[id] })
	slices.Sort(ids)
	return ids, nil
}

func (s *ClientService) resetTrafficBatch(ctx context.Context, inboundSvc *InboundService, scope string, emails []string, requestID string) (int, bool, error) {
	operation, err := captureClientTrafficResetBatch(ctx, scope, emails, requestID)
	if err != nil {
		return 0, false, err
	}
	return s.applyTrafficResetBatch(ctx, inboundSvc, operation)
}

func (s *ClientService) applyTrafficResetBatch(ctx context.Context, inboundSvc *InboundService, operation model.ClientTrafficResetBatch) (int, bool, error) {
	affected, needRestart, effects, err := s.applyTrafficResetBatchLifecycle(ctx, operation)
	// Generic Runtime callbacks acquire the lifecycle lock while inbound edits
	// hold an inbound lock. Apply legacy effects only after releasing lifecycle.
	if effects != nil {
		needRestart = effects.apply(ctx, inboundSvc) || needRestart
	}
	return affected, needRestart, err
}

func (s *ClientService) applyTrafficResetBatchLifecycle(ctx context.Context, operation model.ClientTrafficResetBatch) (affected int, needRestart bool, effects *trafficResetLegacyEffects, resultErr error) {
	if ctx == nil {
		return 0, false, nil, ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return 0, false, nil, err
	}
	lock.Lock()
	defer lock.Unlock()
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return 0, false, nil, err
	}
	expected := database.GetDB()
	state, owned, err := authorityResetExecutionStateLocked(ctx, expected)
	if err != nil {
		return 0, false, nil, err
	}
	if owned {
		defer func() { resultErr = errors.Join(resultErr, state.Journal.Close()) }()
	}
	capture, err := authorityResetExecutionCapture(state, operation)
	if err != nil {
		return 0, false, nil, err
	}
	process := currentXrayProcess()
	if managedAuthorityForProcess(process) != nil {
		defer func() {
			if resultErr != nil && process.IsRunning() {
				stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				resultErr = errors.Join(resultErr, stopManagedProcess(stop, process))
				(&XrayService{}).SetToNeedRestart()
			}
		}()
	}
	return s.applyTrafficResetBatchLocked(ctx, operation, expected, state, capture)
}

func (s *ClientService) applyTrafficResetBatchLocked(ctx context.Context, operation model.ClientTrafficResetBatch, expected *gorm.DB, state *durableAuthorityState, capture *policyauthority.ResetOperationCapture) (int, bool, *trafficResetLegacyEffects, error) {
	scope := operation.Scope
	records, _, err := resolveClientResetTargets(expected.WithContext(ctx), operation.TargetsJSON, false)
	if err != nil {
		return 0, false, nil, err
	}
	managed, err := resetBatchManagedIDs(expected.WithContext(ctx), operation, records)
	if err != nil {
		return 0, false, nil, err
	}
	hash := sha256.Sum256([]byte(operation.RequestID))
	policyRequest := "batch:" + hex.EncodeToString(hash[:])
	var legacy []model.ClientRecord
	var legacyEmails []string
	var enabledInbounds map[int][]string
	var scheduledInbounds []int
	legacyAffected := 0
	committed := false
	fresh := false
	prepare := func(instanceID string) ([]clientpolicy.Policy, error) {
		var policies []clientpolicy.Policy
		err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&operation, "request_id = ?", operation.RequestID).Error; err != nil {
				return err
			}
			if operation.Applied && capture != nil {
				// Older implementations acknowledged per-client changes without
				// an operation preparation. A retry cannot invent their original
				// effect time from its wall clock or a later client's reset stamp.
				_, err := state.Journal.LookupResetPreparation(capture.RequestID)
				if errors.Is(err, policyauthority.ErrNotFound) {
					capture = nil
				} else if err != nil {
					return err
				}
			}
			records, orphans, err := resolveClientResetTargets(tx, operation.TargetsJSON, true)
			if err != nil {
				return err
			}
			current, err := resetBatchManagedIDs(tx, operation, records)
			if err != nil {
				return err
			}
			if !slices.Equal(current, managed) {
				return fmt.Errorf("%w: managed membership changed from %d to %d", ErrClientPolicyLegacyReset, len(managed), len(current))
			}
			activeManaged := managed
			resetAt := time.Now().UnixMilli()
			if operation.ScheduledAt > 0 {
				resetAt = operation.ScheduledAt
				if !operation.Applied {
					records, err = scheduledResetEligibleClients(tx, records, operation, managed)
					if err != nil {
						return err
					}
					activeManaged = nil
					for _, record := range records {
						if _, found := slices.BinarySearch(managed, record.StableID); found {
							activeManaged = append(activeManaged, record.StableID)
						}
					}
				}
			}
			if len(activeManaged) > 0 {
				if operation.Applied {
					for _, batch := range chunkStrings(managed, 1000) {
						var count int64
						if err := tx.Model(&model.ClientPolicyReset{}).Where("client_id IN ? AND request_id = ?", batch, policyRequest).Count(&count).Error; err != nil {
							return err
						}
						if count != int64(len(batch)) {
							return ErrClientPolicyLedger
						}
					}
				}
				policies, err = prepareClientPolicyResetsAtTx(tx, instanceID, activeManaged, policyRequest, resetAt)
				if err != nil {
					return err
				}
			}
			if operation.Applied {
				return prepareAuthorityResetExecutionTx(ctx, tx, state, capture, operation, resetAt)
			}
			legacyEmails = orphans
			for _, record := range records {
				if _, managed := slices.BinarySearch(current, record.StableID); !managed {
					legacy = append(legacy, record)
					legacyEmails = append(legacyEmails, record.Email)
				}
			}
			affected, err := resetLegacyTrafficBatch(tx, legacyEmails)
			if err != nil {
				return err
			}
			legacyIDs := make([]string, len(legacy))
			for i, record := range legacy {
				legacyIDs[i] = record.StableID
			}
			if err := recordClientTrafficResetTimes(tx, legacyIDs, resetAt); err != nil {
				return err
			}
			if scope == "bulk" {
				enabledInbounds, err = enableLegacyResetClients(tx, legacy)
				if err != nil {
					return err
				}
			} else if operation.ScheduledAt > 0 {
				enabledInbounds, scheduledInbounds, err = prepareScheduledLegacyReset(tx, operation, legacy)
				if err != nil {
					return err
				}
			}
			if strings.HasPrefix(scope, "inbound:") && len(records)+len(orphans) > 0 {
				id, _ := strconv.Atoi(strings.TrimPrefix(scope, "inbound:"))
				query := tx.Model(&model.Inbound{}).Where("id = ?", id)
				if id == -1 {
					query = tx.Model(&model.Inbound{}).Where("id > 0")
				}
				if err := query.Update("last_traffic_reset_time", time.Now().UnixMilli()).Error; err != nil {
					return err
				}
			}
			raw, err := json.Marshal(activeManaged)
			if err != nil {
				return err
			}
			operation.Applied, operation.Affected, operation.ManagedIDsJSON = true, affected+len(activeManaged), string(raw)
			legacyAffected = affected
			fresh = true
			if err := tx.Model(&operation).Select("applied", "affected", "managed_ids_json").Updates(&operation).Error; err != nil {
				return err
			}
			return prepareAuthorityResetExecutionTx(ctx, tx, state, capture, operation, resetAt)
		})
		if err != nil {
			return nil, err
		}
		committed = true
		return policies, nil
	}
	if len(managed) > 0 {
		err = applyLocalClientPolicyResetLocked(ctx, managed, prepare)
	} else {
		_, err = prepare("")
	}
	if !committed {
		return 0, false, nil, err
	}
	if err == nil {
		err = completeAuthorityResetExecution(ctx, expected, state, capture)
	}
	return operation.Affected, legacyAffected > 0, &trafficResetLegacyEffects{
		operation: operation, legacy: legacy, emails: legacyEmails,
		enabledInbounds: enabledInbounds, scheduledInbounds: scheduledInbounds, fresh: fresh,
	}, err
}

// SQL preparation and managed acknowledgement are complete before this phase.
// Legacy effects keep their existing runtime error/restart behavior.
type trafficResetLegacyEffects struct {
	operation         model.ClientTrafficResetBatch
	legacy            []model.ClientRecord
	emails            []string
	enabledInbounds   map[int][]string
	scheduledInbounds []int
	fresh             bool
}

func (effects *trafficResetLegacyEffects) apply(ctx context.Context, inboundSvc *InboundService) bool {
	needRestart := false
	for _, email := range effects.emails {
		inboundSvc.resetMtprotoClientQuota(email)
	}
	for _, id := range sortedInboundIds(effects.enabledInbounds) {
		if applyErr := applyLegacyResetEnable(ctx, inboundSvc, id, effects.enabledInbounds[id]); applyErr != nil {
			needRestart = true
			logger.Warning("Failed to apply committed traffic reset enable:", applyErr)
		}
	}
	if effects.fresh && effects.operation.ScheduledAt > 0 {
		applyScheduledLegacyReset(ctx, inboundSvc, effects.operation, effects.legacy, effects.scheduledInbounds)
	}
	return needRestart
}

func resetLegacyTrafficBatch(tx *gorm.DB, emails []string) (int, error) {
	if len(emails) == 0 {
		return 0, nil
	}
	if err := adjustGroupBaselinesForRemovedTraffic(tx, emails); err != nil {
		return 0, err
	}
	affected := 0
	for _, batch := range chunkStrings(emails, sqlInChunk) {
		result := tx.Model(&xray.ClientTraffic{}).Where("email IN ?", batch).Updates(map[string]any{"enable": true, "up": 0, "down": 0})
		if result.Error != nil {
			return 0, result.Error
		}
		affected += int(result.RowsAffected)
		if err := clearGlobalTraffic(tx, batch...); err != nil {
			return 0, err
		}
		if err := tx.Where("email IN ?", batch).Delete(&model.NodeClientTraffic{}).Error; err != nil {
			return 0, err
		}
	}
	return affected, nil
}
