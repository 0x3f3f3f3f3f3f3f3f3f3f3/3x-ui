package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
)

const authorityDirectResetPrefix = "policy-reset:"

type authorityDirectResetCaptureSnapshot struct {
	Schema    int
	RequestID string
	ClientIDs []string
}

type authorityDirectResetPreparationSnapshot struct {
	Schema    int
	RequestID string
	Resets    []model.ClientPolicyReset
}

func authorityDirectResetKey(request string, ids []string) string {
	raw, _ := json.Marshal([]any{request, ids})
	return authorityDirectResetPrefix + authorityResetSnapshotDigest(string(raw))
}

func decodeAuthorityDirectResetCapture(capture policyauthority.ResetOperationCapture, journal *policyauthority.Journal, source string) (authorityDirectResetCaptureSnapshot, error) {
	var snapshot authorityDirectResetCaptureSnapshot
	if journal == nil || capture.Identity != journal.Identity() || capture.SourceID != source || capture.CalendarKey != "" {
		return snapshot, ErrClientPolicyLedger
	}
	decoder := json.NewDecoder(strings.NewReader(capture.Snapshot))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || snapshot.Schema != 1 || !validPolicySourceKey(snapshot.RequestID) || len(snapshot.ClientIDs) == 0 || len(snapshot.ClientIDs) > 100000 {
		return snapshot, ErrClientPolicyLedger
	}
	for i, id := range snapshot.ClientIDs {
		if uuid.Validate(id) != nil || i > 0 && id <= snapshot.ClientIDs[i-1] {
			return snapshot, ErrClientPolicyLedger
		}
	}
	if capture.RequestID != authorityDirectResetKey(snapshot.RequestID, snapshot.ClientIDs) {
		return snapshot, ErrClientPolicyLedger
	}
	return snapshot, nil
}

func decodeAuthorityDirectResetPreparation(prepared policyauthority.ResetOperationPreparation, capture policyauthority.ResetOperationCapture, journal *policyauthority.Journal, source string) (authorityDirectResetPreparationSnapshot, error) {
	var snapshot authorityDirectResetPreparationSnapshot
	original, err := decodeAuthorityDirectResetCapture(capture, journal, source)
	if err != nil {
		return snapshot, err
	}
	if prepared.Identity != capture.Identity || prepared.SourceID != source || prepared.RequestID != capture.RequestID || prepared.CaptureDigest != authorityResetSnapshotDigest(capture.Snapshot) {
		return snapshot, ErrClientPolicyLedger
	}
	decoder := json.NewDecoder(strings.NewReader(prepared.Snapshot))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || snapshot.Schema != 1 || snapshot.RequestID != original.RequestID || len(snapshot.Resets) != len(original.ClientIDs) {
		return snapshot, ErrClientPolicyLedger
	}
	for i, reset := range snapshot.Resets {
		if validateClientPolicyReset(&reset) != nil || reset.Id != 0 || reset.CreatedAt < 0 || reset.InstanceID != source || reset.ClientID != original.ClientIDs[i] || reset.RequestID != original.RequestID || reset.PolicyVersion <= 0 {
			return snapshot, ErrClientPolicyLedger
		}
	}
	return snapshot, nil
}

func directResetRowsTx(tx *gorm.DB, ids []string, request string) ([]model.ClientPolicyReset, error) {
	var resets []model.ClientPolicyReset
	for _, batch := range chunkStrings(ids, 1000) {
		var rows []model.ClientPolicyReset
		if err := tx.Where("client_id IN ? AND request_id = ?", batch, request).Order("client_id").Find(&rows).Error; err != nil {
			return nil, err
		}
		for i := range rows {
			rows[i].Id = 0
		}
		resets = append(resets, rows...)
	}
	return resets, nil
}

func validateAuthorityDirectResetSourceTx(tx *gorm.DB, source string) error {
	var current model.ClientPolicySource
	if err := tx.Where("node_key = ?", "local").First(&current).Error; err != nil {
		return err
	}
	if current.InstanceID != source {
		return ErrClientPolicyLedger
	}
	return nil
}

// Caller owns lifecycle and the pinned SQL transaction. A raw request can have
// older protected per-client history even when its SQL rows/envelope are absent.
func captureAuthorityDirectResetTx(tx *gorm.DB, state *durableAuthorityState, ids []string, request string) (*policyauthority.ResetOperationCapture, error) {
	if state == nil {
		return nil, nil
	}
	if err := validateAuthorityDirectResetSourceTx(tx, state.SourceID); err != nil {
		return nil, err
	}
	for _, batch := range chunkStrings(ids, 1000) {
		if err := validateLocalClientPolicyResetScope(tx, batch); err != nil {
			return nil, err
		}
	}
	for _, id := range ids {
		account, err := state.Journal.LookupAccount(id)
		if err != nil {
			return nil, err
		}
		if err := recoverAuthorityResetHistoryTx(tx, state.Journal, state.SourceID, account); err != nil {
			return nil, err
		}
	}
	key := authorityDirectResetKey(request, ids)
	capture, err := state.Journal.LookupResetOperation(key)
	if err == nil {
		original, err := decodeAuthorityDirectResetCapture(capture, state.Journal, state.SourceID)
		if err != nil || original.RequestID != request || !slices.Equal(original.ClientIDs, ids) {
			return nil, ErrClientPolicyLedger
		}
		if err := recoverAuthorityDirectPreparedResetTx(tx, state.Journal, state.SourceID, capture); err != nil {
			return nil, err
		}
		return &capture, nil
	}
	if !errors.Is(err, policyauthority.ErrNotFound) {
		return nil, err
	}
	rows, err := directResetRowsTx(tx, ids, request)
	if err != nil {
		return nil, err
	}
	if len(rows) == len(ids) {
		// Keep the prior acknowledged compatibility path. Historical rows are
		// not permission to invent a newer effect time or operation witness.
		return nil, nil
	}
	var count int64
	for _, batch := range chunkStrings(ids, 1000) {
		var found int64
		if err := tx.Model(&model.ClientRecord{}).Where("stable_id IN ?", batch).Count(&found).Error; err != nil {
			return nil, err
		}
		count += found
	}
	if count != int64(len(ids)) {
		return nil, gorm.ErrRecordNotFound
	}
	raw, err := json.Marshal(authorityDirectResetCaptureSnapshot{Schema: 1, RequestID: request, ClientIDs: ids})
	if err != nil {
		return nil, err
	}
	capture = policyauthority.ResetOperationCapture{Identity: state.Journal.Identity(), SourceID: state.SourceID, RequestID: key, Snapshot: string(raw)}
	if _, err := decodeAuthorityDirectResetCapture(capture, state.Journal, state.SourceID); err != nil {
		return nil, err
	}
	if err := state.Journal.CaptureResetOperation(capture); err != nil {
		return nil, err
	}
	return &capture, nil
}

func prepareAuthorityDirectResetTx(tx *gorm.DB, state *durableAuthorityState, capture *policyauthority.ResetOperationCapture) error {
	if capture == nil {
		return nil
	}
	if err := validateAuthorityDirectResetSourceTx(tx, state.SourceID); err != nil {
		return err
	}
	original, err := decodeAuthorityDirectResetCapture(*capture, state.Journal, state.SourceID)
	if err != nil {
		return err
	}
	rows, err := directResetRowsTx(tx, original.ClientIDs, original.RequestID)
	if err != nil {
		return err
	}
	previous, err := state.Journal.LookupResetPreparation(capture.RequestID)
	if err == nil {
		snapshot, err := decodeAuthorityDirectResetPreparation(previous, *capture, state.Journal, state.SourceID)
		if err != nil {
			return err
		}
		if !slices.Equal(snapshot.Resets, rows) {
			return ErrClientPolicyLedger
		}
		return nil
	}
	if !errors.Is(err, policyauthority.ErrNotFound) {
		return err
	}
	raw, err := json.Marshal(authorityDirectResetPreparationSnapshot{Schema: 1, RequestID: original.RequestID, Resets: rows})
	if err != nil {
		return err
	}
	prepared := policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: string(raw)}
	if _, err := decodeAuthorityDirectResetPreparation(prepared, *capture, state.Journal, state.SourceID); err != nil {
		return err
	}
	return state.Journal.PrepareResetOperation(prepared)
}

func recoverAuthorityDirectPreparedResetTx(tx *gorm.DB, journal *policyauthority.Journal, source string, capture policyauthority.ResetOperationCapture) error {
	prepared, err := journal.LookupResetPreparation(capture.RequestID)
	if errors.Is(err, policyauthority.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	snapshot, err := decodeAuthorityDirectResetPreparation(prepared, capture, journal, source)
	if err != nil {
		return err
	}
	return recoverAuthorityPreparedResetRowsTx(tx, journal, source, snapshot.Resets, 0)
}

func applyAuthorityDirectReset(ctx context.Context, ids []string, request string) (resultErr error) {
	if ctx == nil {
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
	expected := database.GetDB()
	state, owned, err := authorityResetExecutionStateLocked(ctx, expected)
	if err != nil {
		return err
	}
	if owned {
		defer func() { resultErr = errors.Join(resultErr, state.Journal.Close()) }()
	}
	process := currentXrayProcess()
	var capture *policyauthority.ResetOperationCapture
	if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		var err error
		capture, err = captureAuthorityDirectResetTx(tx, state, ids, request)
		return err
	}); err != nil {
		return err
	}
	err = applyLocalClientPolicyResetLocked(ctx, ids, func(instanceID string) ([]clientpolicy.Policy, error) {
		if state != nil && instanceID != state.SourceID {
			return nil, ErrClientPolicyLedger
		}
		var policies []clientpolicy.Policy
		err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
			var err error
			policies, err = prepareClientPolicyResetsAtTx(tx, instanceID, ids, request, time.Now().UnixMilli())
			if err != nil {
				return err
			}
			return prepareAuthorityDirectResetTx(tx, state, capture)
		})
		return policies, err
	})
	if err != nil || capture == nil {
		return err
	}
	// The private helper owns cleanup once execution starts. Only a failure
	// persisting completion after its successful application needs outer cleanup.
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
	return runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		if err := validateAuthorityDirectResetSourceTx(tx, state.SourceID); err != nil {
			return err
		}
		prepared, err := state.Journal.LookupResetPreparation(capture.RequestID)
		if err != nil {
			return err
		}
		if _, err := decodeAuthorityDirectResetPreparation(prepared, *capture, state.Journal, state.SourceID); err != nil {
			return err
		}
		return state.Journal.CompleteResetOperation(policyauthority.ResetOperationCompletion{Identity: prepared.Identity, SourceID: prepared.SourceID, RequestID: prepared.RequestID, PreparationDigest: authorityResetSnapshotDigest(prepared.Snapshot)})
	})
}
