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
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const authorityResetRequestPrefix = "traffic-reset:"

type authorityResetCaptureSnapshot struct {
	Schema             int
	Operation          model.ClientTrafficResetBatch
	OriginalManagedIDs []string
}

func authorityResetRequestKey(request string) string {
	digest := sha256.Sum256([]byte(request))
	return authorityResetRequestPrefix + hex.EncodeToString(digest[:])
}

func authorityResetCalendarKey(scope string, at int64) string {
	if at == 0 {
		return ""
	}
	digest := sha256.Sum256([]byte("traffic-calendar:" + scope + "/" + strconv.FormatInt(at, 10)))
	return hex.EncodeToString(digest[:])
}

func decodeAuthorityResetCapture(capture policyauthority.ResetOperationCapture, journal *policyauthority.Journal, source string) (authorityResetCaptureSnapshot, error) {
	var snapshot authorityResetCaptureSnapshot
	if journal == nil || capture.Identity != journal.Identity() || capture.SourceID != source {
		return snapshot, ErrClientPolicyLedger
	}
	decoder := json.NewDecoder(strings.NewReader(capture.Snapshot))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || snapshot.Schema != 1 || !validAuthorityResetBatch(snapshot.Operation) || capture.RequestID != authorityResetRequestKey(snapshot.Operation.RequestID) || capture.CalendarKey != authorityResetCalendarKey(snapshot.Operation.Scope, snapshot.Operation.ScheduledAt) || len(snapshot.OriginalManagedIDs) > 100000 {
		return snapshot, ErrClientPolicyLedger
	}
	var targets []clientResetTarget
	if json.Unmarshal([]byte(snapshot.Operation.TargetsJSON), &targets) != nil {
		return snapshot, ErrClientPolicyLedger
	}
	members := make(map[string]bool, len(targets))
	for _, target := range targets {
		if target.ClientID != "" {
			members[target.ClientID] = true
		}
	}
	for i, id := range snapshot.OriginalManagedIDs {
		if !members[id] || i > 0 && id <= snapshot.OriginalManagedIDs[i-1] {
			return snapshot, ErrClientPolicyLedger
		}
	}
	return snapshot, nil
}

func authorityResetCaptureHasLegacy(snapshot authorityResetCaptureSnapshot) bool {
	var inboundIDs []int
	if json.Unmarshal([]byte(snapshot.Operation.InboundIDsJSON), &inboundIDs) != nil || len(inboundIDs) != 0 {
		// Calendar inbound counters and remote resets are effects even when
		// the client selection is empty or contains only managed identities.
		return true
	}
	managed := make(map[string]bool, len(snapshot.OriginalManagedIDs))
	for _, id := range snapshot.OriginalManagedIDs {
		managed[id] = true
	}
	var targets []clientResetTarget
	if json.Unmarshal([]byte(snapshot.Operation.TargetsJSON), &targets) != nil {
		return true
	}
	for _, target := range targets {
		if !managed[target.ClientID] {
			return true
		}
	}
	return false
}

func recoverAuthorityCapturedResetTx(tx *gorm.DB, snapshot authorityResetCaptureSnapshot) error {
	if !snapshot.Operation.Applied && authorityResetCaptureHasLegacy(snapshot) {
		var current model.ClientTrafficResetBatch
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "request_id = ?", snapshot.Operation.RequestID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && !current.Applied {
			// Selection alone cannot distinguish unapplied client/inbound effects
			// from a restored SQL acknowledgement. Exact preparation follows.
			return ErrClientPolicyLedger
		}
		if err != nil {
			return err
		}
	}
	return recoverAuthorityResetBatchTx(tx, snapshot.Operation)
}

// Caller holds the lifecycle lock. Opening retained state never initializes it.
func resetCaptureStateLocked(expected *gorm.DB) (*durableAuthorityState, error) {
	dir := filepath.Join(config.GetDBFolderPath(), "client-policy", "authority")
	required := false
	if process := currentXrayProcess(); process != nil && len(process.GetConfig().ClientPolicy) > 0 {
		var policy conf.ClientPolicyConfig
		if json.Unmarshal(process.GetConfig().ClientPolicy, &policy) != nil || !filepath.IsAbs(policy.StateFile) {
			return nil, ErrClientPolicyLedger
		}
		if process.IsRunning() {
			return nil, ErrAuthorityNotInitialized
		}
		dir = filepath.Join(filepath.Dir(policy.StateFile), "authority")
		required = true
	}
	_, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) && !required {
		var count int64
		if err := expected.Model(&model.ClientPolicyAuthorityProjection{}).Limit(1).Count(&count).Error; err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, nil
		}
	}
	if err != nil {
		return nil, ErrAuthorityNotInitialized
	}
	return openAuthorityState(dir)
}

func runAuthorityResetCapture(ctx context.Context, requestKey, calendarKey string, operation *model.ClientTrafficResetBatch, selectOperation func(*gorm.DB) error, validateSelection func(model.ClientTrafficResetBatch) error) (result error) {
	if ctx == nil || operation == nil || selectOperation == nil || validateSelection == nil {
		return ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return err
	}
	expected := database.GetDB()
	var state *durableAuthorityState
	if owner := managedAuthorityForProcess(currentXrayProcess()); owner != nil {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if owner.closed || owner.db != expected || owner.state.SourceID != owner.config.InstanceID {
			return ErrClientPolicyLedger
		}
		state = owner.state
	} else {
		var err error
		state, err = resetCaptureStateLocked(expected.WithContext(ctx))
		if err != nil {
			return err
		}
		if state != nil {
			defer func() { result = errors.Join(result, state.Journal.Close()) }()
		}
	}
	return runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		if state == nil {
			return selectOperation(tx)
		}
		var source model.ClientPolicySource
		if err := tx.Where("node_key = ?", "local").First(&source).Error; err != nil {
			return err
		}
		if source.InstanceID != state.SourceID {
			return ErrClientPolicyLedger
		}
		journal := state.Journal
		var capture policyauthority.ResetOperationCapture
		var err error
		if calendarKey != "" {
			capture, err = journal.LookupResetCalendar(calendarKey)
		} else {
			capture, err = journal.LookupResetOperation(requestKey)
		}
		if err == nil {
			snapshot, err := decodeAuthorityResetCapture(capture, journal, state.SourceID)
			if err != nil {
				return err
			}
			if err := validateSelection(snapshot.Operation); err != nil {
				return err
			}
			if err := recoverAuthorityCapturedResetTx(tx, snapshot); err != nil {
				return err
			}
			if err := recoverAuthorityPreparedResetTx(tx, journal, state.SourceID, capture); err != nil {
				return err
			}
			return tx.First(operation, "request_id = ?", snapshot.Operation.RequestID).Error
		}
		if !errors.Is(err, policyauthority.ErrNotFound) {
			return err
		}
		if err := selectOperation(tx); err != nil {
			return err
		}
		if !validAuthorityResetBatch(*operation) {
			return ErrClientPolicyLedger
		}
		records, _, err := resolveClientResetTargets(tx, operation.TargetsJSON, false)
		if err != nil {
			return err
		}
		managed, err := managedClientResetIDs(tx, records)
		if err != nil {
			return err
		}
		snapshot := authorityResetCaptureSnapshot{Schema: 1, Operation: *operation, OriginalManagedIDs: managed}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		return journal.CaptureResetOperation(policyauthority.ResetOperationCapture{Identity: journal.Identity(), SourceID: state.SourceID, RequestID: authorityResetRequestKey(operation.RequestID), CalendarKey: authorityResetCalendarKey(operation.Scope, operation.ScheduledAt), Snapshot: string(raw)})
	})
}

// Startup owns the lifecycle and private journal. Restore metadata only, one
// bounded snapshot at a time; existing policy/account recovery follows this.
func recoverAuthorityResetCaptures(ctx context.Context, expected *gorm.DB, journal *policyauthority.Journal, source string) error {
	if ctx == nil || expected == nil || journal == nil || !validPolicySourceKey(source) {
		return ErrClientPolicyLedger
	}
	validateSource := func(tx *gorm.DB) error {
		var current model.ClientPolicySource
		if err := tx.Where("node_key = ?", "local").First(&current).Error; err != nil {
			return err
		}
		if current.InstanceID != source {
			return ErrClientPolicyLedger
		}
		return nil
	}
	if err := runSerializedTxContextForDatabase(ctx, expected, validateSource); err != nil {
		return err
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := journal.ResetOperationPage(after, 128)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, summary := range page {
			direct := strings.HasPrefix(summary.RequestID, authorityDirectResetPrefix)
			if !direct && !strings.HasPrefix(summary.RequestID, authorityResetRequestPrefix) {
				continue
			}
			capture, err := journal.LookupResetOperation(summary.RequestID)
			if err != nil {
				return err
			}
			if direct {
				if _, err := decodeAuthorityDirectResetCapture(capture, journal, source); err != nil {
					return err
				}
				if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
					if err := validateSource(tx); err != nil {
						return err
					}
					return recoverAuthorityDirectPreparedResetTx(tx, journal, source, capture)
				}); err != nil {
					return err
				}
				continue
			}
			snapshot, err := decodeAuthorityResetCapture(capture, journal, source)
			if err != nil {
				return err
			}
			if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
				if err := validateSource(tx); err != nil {
					return err
				}
				if err := recoverAuthorityCapturedResetTx(tx, snapshot); err != nil {
					return err
				}
				return recoverAuthorityPreparedResetTx(tx, journal, source, capture)
			}); err != nil {
				return err
			}
		}
		after = page[len(page)-1].RequestID
	}
}
