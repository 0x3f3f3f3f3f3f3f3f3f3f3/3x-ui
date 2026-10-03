package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

type authorityResetPreparationSnapshot struct {
	Schema           int
	RequestID        string
	ResetAt          int64
	ActiveManagedIDs []string
	Affected         int
	Resets           []model.ClientPolicyReset
}

func authorityResetSnapshotDigest(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

// Caller owns the lifecycle lock. Private restricted pipeline fixtures have no
// retained owner; public callers have already passed source-owned admission.
func authorityResetExecutionStateLocked(ctx context.Context, expected *gorm.DB) (*durableAuthorityState, bool, error) {
	if owner := managedAuthorityForProcess(currentXrayProcess()); owner != nil {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if owner.closed || owner.db != expected || owner.config.InstanceID != owner.state.SourceID {
			return nil, false, ErrClientPolicyLedger
		}
		return owner.state, false, nil
	}
	if process := currentXrayProcess(); process != nil && process.IsRunning() {
		dir := filepath.Join(config.GetDBFolderPath(), "client-policy", "authority")
		if raw := process.GetConfig().ClientPolicy; len(raw) > 0 {
			var policy conf.ClientPolicyConfig
			if json.Unmarshal(raw, &policy) != nil || !filepath.IsAbs(policy.StateFile) {
				return nil, false, ErrClientPolicyLedger
			}
			dir = filepath.Join(filepath.Dir(policy.StateFile), "authority")
		}
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			return nil, false, ErrAuthorityNotInitialized
		}
		var count int64
		if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
			return tx.Model(&model.ClientPolicyAuthorityProjection{}).Limit(1).Count(&count).Error
		}); err != nil {
			return nil, false, err
		}
		if count != 0 {
			return nil, false, ErrAuthorityNotInitialized
		}
		return nil, false, nil
	}
	state, err := resetCaptureStateLocked(expected.WithContext(ctx))
	return state, state != nil, err
}

func authorityResetExecutionCapture(state *durableAuthorityState, operation model.ClientTrafficResetBatch) (*policyauthority.ResetOperationCapture, error) {
	if state == nil {
		return nil, nil
	}
	capture, err := state.Journal.LookupResetOperation(authorityResetRequestKey(operation.RequestID))
	if err != nil {
		return nil, err
	}
	snapshot, err := decodeAuthorityResetCapture(capture, state.Journal, state.SourceID)
	if err != nil {
		return nil, err
	}
	if snapshot.Operation.Scope != operation.Scope || snapshot.Operation.ScheduledAt != operation.ScheduledAt || snapshot.Operation.TargetsJSON != operation.TargetsJSON || snapshot.Operation.InboundIDsJSON != operation.InboundIDsJSON || snapshot.Operation.SelectionHash != operation.SelectionHash || snapshot.Operation.CreatedAt != operation.CreatedAt {
		return nil, ErrClientPolicyLedger
	}
	if authorityResetCaptureHasLegacy(snapshot) || strings.HasPrefix(operation.Scope, "inbound:") {
		return nil, nil
	}
	return &capture, nil
}

func decodeAuthorityResetPreparation(prepared policyauthority.ResetOperationPreparation, capture policyauthority.ResetOperationCapture, journal *policyauthority.Journal, source string) (authorityResetPreparationSnapshot, error) {
	var snapshot authorityResetPreparationSnapshot
	original, err := decodeAuthorityResetCapture(capture, journal, source)
	if err != nil {
		return snapshot, err
	}
	if prepared.Identity != capture.Identity || prepared.SourceID != source || prepared.RequestID != capture.RequestID || prepared.CaptureDigest != authorityResetSnapshotDigest(capture.Snapshot) || authorityResetCaptureHasLegacy(original) || strings.HasPrefix(original.Operation.Scope, "inbound:") {
		return snapshot, ErrClientPolicyLedger
	}
	decoder := json.NewDecoder(strings.NewReader(prepared.Snapshot))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || snapshot.Schema != 1 || snapshot.RequestID != original.Operation.RequestID || snapshot.ResetAt <= 0 || snapshot.Affected != len(snapshot.ActiveManagedIDs) || len(snapshot.ActiveManagedIDs) > 100000 || len(snapshot.Resets) != len(snapshot.ActiveManagedIDs) {
		return snapshot, ErrClientPolicyLedger
	}
	if original.Operation.ScheduledAt > 0 && snapshot.ResetAt != original.Operation.ScheduledAt {
		return snapshot, ErrClientPolicyLedger
	}
	request := "batch:" + authorityResetSnapshotDigest(original.Operation.RequestID)
	for i, id := range snapshot.ActiveManagedIDs {
		if _, ok := slices.BinarySearch(original.OriginalManagedIDs, id); !ok || i > 0 && id <= snapshot.ActiveManagedIDs[i-1] {
			return snapshot, ErrClientPolicyLedger
		}
		reset := snapshot.Resets[i]
		if validateClientPolicyReset(&reset) != nil || reset.Id != 0 || reset.CreatedAt < 0 || reset.InstanceID != source || reset.ClientID != id || reset.RequestID != request || reset.PolicyVersion <= 0 {
			return snapshot, ErrClientPolicyLedger
		}
	}
	return snapshot, nil
}

func prepareAuthorityResetExecutionTx(ctx context.Context, tx *gorm.DB, state *durableAuthorityState, capture *policyauthority.ResetOperationCapture, operation model.ClientTrafficResetBatch, resetAt int64) error {
	if capture == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var source model.ClientPolicySource
	if err := tx.Where("node_key = ?", "local").First(&source).Error; err != nil {
		return err
	}
	if source.InstanceID != state.SourceID {
		return ErrClientPolicyLedger
	}
	var ids []string
	if json.Unmarshal([]byte(operation.ManagedIDsJSON), &ids) != nil {
		return ErrClientPolicyLedger
	}
	snapshot := authorityResetPreparationSnapshot{Schema: 1, RequestID: operation.RequestID, ResetAt: resetAt, ActiveManagedIDs: ids, Affected: operation.Affected}
	request := "batch:" + authorityResetSnapshotDigest(operation.RequestID)
	for _, batch := range chunkStrings(ids, 1000) {
		var rows []model.ClientPolicyReset
		if err := tx.Where("client_id IN ? AND request_id = ?", batch, request).Order("client_id").Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			rows[i].Id = 0
		}
		snapshot.Resets = append(snapshot.Resets, rows...)
	}
	previous, err := state.Journal.LookupResetPreparation(capture.RequestID)
	if err == nil {
		original, err := decodeAuthorityResetPreparation(previous, *capture, state.Journal, state.SourceID)
		if err != nil {
			return err
		}
		if original.RequestID != snapshot.RequestID || original.Affected != snapshot.Affected || !slices.Equal(original.ActiveManagedIDs, snapshot.ActiveManagedIDs) || !slices.Equal(original.Resets, snapshot.Resets) {
			return ErrClientPolicyLedger
		}
		return nil
	}
	if !errors.Is(err, policyauthority.ErrNotFound) {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	preparation := policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: authorityResetSnapshotDigest(capture.Snapshot), Snapshot: string(raw)}
	if _, err := decodeAuthorityResetPreparation(preparation, *capture, state.Journal, state.SourceID); err != nil {
		return err
	}
	return state.Journal.PrepareResetOperation(preparation)
}

func completeAuthorityResetExecution(ctx context.Context, expected *gorm.DB, state *durableAuthorityState, capture *policyauthority.ResetOperationCapture) error {
	if capture == nil {
		return nil
	}
	// Keep source validation and the durable acknowledgement within the same
	// pinned SQL handle, after core RPC and outside its preparation transaction.
	return runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		var source model.ClientPolicySource
		if err := tx.Where("node_key = ?", "local").First(&source).Error; err != nil {
			return err
		}
		if source.InstanceID != state.SourceID {
			return ErrClientPolicyLedger
		}
		prepared, err := state.Journal.LookupResetPreparation(capture.RequestID)
		if err != nil {
			return err
		}
		if _, err := decodeAuthorityResetPreparation(prepared, *capture, state.Journal, state.SourceID); err != nil {
			return err
		}
		return state.Journal.CompleteResetOperation(policyauthority.ResetOperationCompletion{Identity: prepared.Identity, SourceID: prepared.SourceID, RequestID: prepared.RequestID, PreparationDigest: authorityResetSnapshotDigest(prepared.Snapshot)})
	})
}

// Preparation is intent, not execution acknowledgement. The journal account
// supplies the known usage ceiling; later committed policy history wins.
func recoverAuthorityPreparedResetTx(tx *gorm.DB, journal *policyauthority.Journal, source string, capture policyauthority.ResetOperationCapture) error {
	prepared, err := journal.LookupResetPreparation(capture.RequestID)
	if errors.Is(err, policyauthority.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	snapshot, err := decodeAuthorityResetPreparation(prepared, capture, journal, source)
	if err != nil {
		return err
	}
	original, err := decodeAuthorityResetCapture(capture, journal, source)
	if err != nil {
		return err
	}
	operation := original.Operation
	operation.Applied, operation.Affected = true, snapshot.Affected
	raw, err := json.Marshal(snapshot.ActiveManagedIDs)
	if err != nil {
		return err
	}
	operation.ManagedIDsJSON = string(raw)
	if err := recoverAuthorityResetBatchTx(tx, operation); err != nil {
		return err
	}
	for _, reset := range snapshot.Resets {
		account, err := journal.LookupAccount(reset.ClientID)
		if err != nil {
			return err
		}
		if uint64(reset.RawUpload) > account.Usage.RawUpload || uint64(reset.RawDownload) > account.Usage.RawDownload || uint64(reset.BilledBytes) > account.Usage.BilledBytes || uint64(reset.BilledBytes) == account.Usage.BilledBytes && uint64(reset.Remainder) > account.Usage.Remainder {
			return ErrClientPolicyLedger
		}
		if err := recoverAuthoritySemanticResetTx(tx, &reset); err != nil {
			return err
		}
		if err := recordClientTrafficResetTimes(tx, []string{reset.ClientID}, snapshot.ResetAt); err != nil {
			return err
		}
		if account.Deleted {
			continue
		}
		var client model.ClientRecord
		err = tx.Where("stable_id = ?", reset.ClientID).First(&client).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err := validateLocalClientPolicyResetScope(tx, []string{reset.ClientID}); err != nil {
			return err
		}
		// Acknowledged history below restores its own version/fingerprint.
		// Installing the user's changed business fields at that same version
		// would conflict with the retained core policy instead of advancing it.
		if client.DesiredPolicyVersion < reset.PolicyVersion && account.Policy.Version < uint64(reset.PolicyVersion) {
			_, fingerprint, err := fingerprintClientPolicy(client, &reset)
			if err != nil {
				return err
			}
			if err := tx.Table("clients").Where("id = ?", client.Id).Updates(map[string]any{"desired_policy_version": reset.PolicyVersion, "policy_fingerprint": fingerprint}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
