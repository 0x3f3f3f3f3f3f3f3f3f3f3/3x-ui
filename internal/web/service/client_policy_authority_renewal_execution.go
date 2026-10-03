package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

func selectAuthorityRenewalTriggersTx(tx *gorm.DB, ids []string, now int64) ([]authorityRenewalTrigger, error) {
	var triggers []authorityRenewalTrigger
	err := tx.Table("clients c").Select("c.stable_id AS client_id, c.expiry_time, COALESCE(ct.reset_count, 0) AS reset_count, c.reset, c.reset_day, c.reset_weekday, c.reset_max").
		Joins("LEFT JOIN client_traffics ct ON ct.email = c.email").
		Where("c.stable_id IN ? AND c.expiry_time > 0 AND c.expiry_time <= ? AND (c.reset > 0 OR c.reset_day > 0 OR c.reset_weekday > 0)", ids, now).
		Where("c.reset_max <= 0 OR COALESCE(ct.reset_count, 0) < c.reset_max").Order("c.stable_id").Scan(&triggers).Error
	return triggers, err
}

func captureAuthorityClientRenewalTx(tx *gorm.DB, state *durableAuthorityState, triggers []authorityRenewalTrigger, now int64, location *time.Location) (*policyauthority.ResetOperationCapture, error) {
	if state == nil || len(triggers) == 0 {
		return nil, nil
	}
	if err := validateAuthorityDirectResetSourceTx(tx, state.SourceID); err != nil {
		return nil, err
	}
	snapshot := authorityRenewalCaptureSnapshot{Schema: 1, At: now, Zone: location.String(), Triggers: triggers}
	ids := authorityRenewalIDs(snapshot)
	if err := validateLocalClientPolicyResetScope(tx, ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		account, err := state.Journal.LookupAccount(id)
		if err != nil || account.Deleted {
			if err == nil {
				err = ErrClientPolicyLedger
			}
			return nil, err
		}
	}
	key := authorityRenewalKey(state.SourceID, snapshot.Zone, triggers)
	capture, err := state.Journal.LookupResetOperation(key)
	if err == nil {
		original, err := decodeAuthorityRenewalCapture(capture, state.Journal, state.SourceID)
		if err != nil || !slices.Equal(original.Triggers, triggers) || original.Zone != snapshot.Zone {
			return nil, ErrClientPolicyLedger
		}
		return &capture, nil
	}
	if !errors.Is(err, policyauthority.ErrNotFound) {
		return nil, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	capture = policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: state.SourceID, RequestID: key, Snapshot: string(raw)}
	if _, err := decodeAuthorityRenewalCapture(capture, state.Journal, state.SourceID); err != nil {
		return nil, err
	}
	if err := state.Journal.CaptureResetOperation(capture); err != nil {
		return nil, err
	}
	return &capture, nil
}

func applyAuthorityClientRenewalBatch(ctx context.Context, process *xray.Process, ids []string, now int64, location *time.Location) (resultErr error) {
	if ctx == nil || process == nil || location == nil || now <= 0 || len(ids) > 1000 {
		return ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return err
	}
	if process != currentXrayProcess() {
		return ErrClientPolicyLedger
	}
	expected := database.GetDB()
	var triggers []authorityRenewalTrigger
	if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		var err error
		triggers, err = selectAuthorityRenewalTriggersTx(tx, ids, now)
		return err
	}); err != nil || len(triggers) == 0 {
		return err
	}
	state, owned, err := authorityResetExecutionStateLocked(ctx, expected)
	if err != nil {
		return err
	}
	if owned {
		defer func() { resultErr = errors.Join(resultErr, state.Journal.Close()) }()
	}
	var capture *policyauthority.ResetOperationCapture
	if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		var err error
		capture, err = captureAuthorityClientRenewalTx(tx, state, triggers, now, location)
		return err
	}); err != nil {
		return err
	}
	if capture != nil {
		return applyAuthorityCapturedRenewalLocked(ctx, process, expected, state, *capture)
	}
	// Restricted lower-pipeline fixtures have no required retained authority.
	// Existing marker/projection admission above never falls back after owner loss.
	var policy conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &policy); err != nil {
		return err
	}
	due := authorityRenewalIDs(authorityRenewalCaptureSnapshot{Triggers: triggers})
	return applyLocalClientPolicyResetLocked(ctx, due, func(source string) ([]clientpolicy.Policy, error) {
		if source != policy.InstanceID {
			return nil, ErrClientPolicyLedger
		}
		var policies []clientpolicy.Policy
		err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
			var err error
			policies, err = prepareClientPolicyRenewals(tx, source, due, now, location)
			return err
		})
		return policies, err
	})
}

func applyAuthorityCapturedRenewalLocked(ctx context.Context, process *xray.Process, expected *gorm.DB, state *durableAuthorityState, capture policyauthority.ResetOperationCapture) (resultErr error) {
	original, err := decodeAuthorityRenewalCapture(capture, state.Journal, state.SourceID)
	if err != nil {
		return err
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil || config.InstanceID != state.SourceID {
		return ErrClientPolicyLedger
	}
	configured := make(map[string]bool, len(config.Policies))
	for _, policy := range config.Policies {
		configured[policy.ClientID] = true
	}
	var active []string
	if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		var clients []model.ClientRecord
		if err := tx.Where("stable_id IN ?", authorityRenewalIDs(original)).Order("stable_id").Find(&clients).Error; err != nil {
			return err
		}
		for _, client := range clients {
			if configured[client.StableID] {
				active = append(active, client.StableID)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	err = applyLocalClientPolicyResetLocked(ctx, active, func(source string) ([]clientpolicy.Policy, error) {
		if source != state.SourceID {
			return nil, ErrClientPolicyLedger
		}
		var policies []clientpolicy.Policy
		err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
			var err error
			policies, err = prepareAuthorityClientRenewalsTx(tx, state, capture, active)
			return err
		})
		return policies, err
	})
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil && process.IsRunning() {
			stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, stopManagedProcess(stop, process))
			(&XrayService{}).SetToNeedRestart()
		}
	}()
	return runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		if err := validateAuthorityDirectResetSourceTx(tx, state.SourceID); err != nil {
			return err
		}
		prepared, err := state.Journal.LookupResetPreparation(capture.RequestID)
		if err != nil {
			return err
		}
		if _, err := decodeAuthorityRenewalPreparation(prepared, capture, state.Journal, state.SourceID); err != nil {
			return err
		}
		return state.Journal.CompleteResetOperation(policyauthority.ResetOperationCompletion{Identity: prepared.Identity, SourceID: prepared.SourceID, RequestID: prepared.RequestID, PreparationDigest: authorityResetSnapshotDigest(prepared.Snapshot)})
	})
}

func resumeAuthorityClientRenewals(ctx context.Context, process *xray.Process) error {
	if managedAuthorityForProcess(process) == nil {
		return nil
	}
	lock.Lock()
	defer lock.Unlock()
	if process != currentXrayProcess() || ctx == nil {
		return ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	expected := database.GetDB()
	state, owned, err := authorityResetExecutionStateLocked(ctx, expected)
	if err != nil || state == nil || owned {
		if err == nil {
			err = ErrClientPolicyLedger
		}
		return err
	}
	after := ""
	for {
		page, err := state.Journal.ResetOperationPage(after, 128)
		if err != nil {
			return err
		}
		for _, header := range page {
			after = header.RequestID
			if !strings.HasPrefix(header.RequestID, authorityRenewalPrefix) {
				continue
			}
			capture, err := state.Journal.LookupResetOperation(header.RequestID)
			if err != nil {
				return err
			}
			if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
				return recoverAuthorityPreparedRenewalTx(tx, state.Journal, state.SourceID, capture)
			}); err != nil {
				return err
			}
			if _, err := state.Journal.LookupResetCompletion(header.RequestID); err == nil {
				continue
			} else if !errors.Is(err, policyauthority.ErrNotFound) {
				return err
			}
			if err := applyAuthorityCapturedRenewalLocked(ctx, process, expected, state, capture); err != nil {
				return err
			}
		}
		if len(page) < 128 {
			return nil
		}
	}
}
