package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type authorityStartupOperation struct {
	prepared policyauthority.ResetOperationPreparation
	resets   []model.ClientPolicyReset
	clients  []string
	stamps   []authorityResetInboundStamp
	renewals []authorityRenewalEffect
}

func (a *managedAuthority) pendingStartupOperation(key string) (*authorityStartupOperation, error) {
	if !strings.HasPrefix(key, authorityDirectResetPrefix) && !strings.HasPrefix(key, authorityResetRequestPrefix) && !strings.HasPrefix(key, authorityRenewalPrefix) {
		return nil, nil
	}
	if _, err := a.state.Journal.LookupResetCompletion(key); err == nil {
		return nil, nil
	} else if !errors.Is(err, policyauthority.ErrNotFound) {
		return nil, err
	}
	prepared, err := a.state.Journal.LookupResetPreparation(key)
	if errors.Is(err, policyauthority.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	capture, err := a.state.Journal.LookupResetOperation(key)
	if err != nil {
		return nil, err
	}
	op := &authorityStartupOperation{prepared: prepared}
	switch {
	case strings.HasPrefix(key, authorityDirectResetPrefix):
		snapshot, err := decodeAuthorityDirectResetPreparation(prepared, capture, a.state.Journal, a.state.SourceID)
		if err != nil {
			return nil, err
		}
		op.resets = snapshot.Resets
		for _, row := range snapshot.Resets {
			op.clients = append(op.clients, row.ClientID)
		}
	case strings.HasPrefix(key, authorityRenewalPrefix):
		snapshot, err := decodeAuthorityRenewalPreparation(prepared, capture, a.state.Journal, a.state.SourceID)
		if err != nil {
			return nil, err
		}
		op.resets = snapshot.Resets
		op.renewals = snapshot.Effects
		original, err := decodeAuthorityRenewalCapture(capture, a.state.Journal, a.state.SourceID)
		if err != nil {
			return nil, err
		}
		op.clients = authorityRenewalIDs(original)
	default:
		snapshot, err := decodeAuthorityResetPreparation(prepared, capture, a.state.Journal, a.state.SourceID)
		if err != nil {
			return nil, err
		}
		op.resets, op.clients = snapshot.Resets, snapshot.ActiveManagedIDs
		op.stamps = snapshot.InboundStamps
	}
	return op, nil
}

// Caller owns the lifecycle and authority locks. The socket and RPC binding
// identify the same live incarnation, not a previously cached readiness flag.
func (a *managedAuthority) validateStartupOwner(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.closed || a.api == nil || a.process != currentXrayProcess() || managedAuthorityForProcess(a.process) != a || !a.process.IsRunning() || !a.process.IsControlReady() || a.config.InstanceID != a.state.SourceID {
		return ErrClientPolicyLedger
	}
	if err := a.validateExecutionRole(); err != nil {
		return err
	}
	if a.delegated() != (a.controller == nil) {
		return ErrClientPolicyLedger
	}
	current, err := os.Lstat(a.socketPath)
	if err != nil || a.socketInfo == nil || current.Mode()&os.ModeSocket == 0 || !os.SameFile(current, a.socketInfo) || a.socketBoot != a.api.Capabilities().BootId {
		return errors.Join(ErrClientPolicyLedger, err)
	}
	return nil
}

func (a *managedAuthority) validateStartupOperationTx(tx *gorm.DB, op *authorityStartupOperation, wanted map[string]clientpolicy.Policy) error {
	if err := validateAuthorityDirectResetSourceTx(tx, a.state.SourceID); err != nil {
		return err
	}
	ids := make([]string, len(op.stamps))
	for i, stamp := range op.stamps {
		ids[i] = stamp.StableID
	}
	for _, batch := range chunkStrings(ids, 1000) {
		var stale int64
		if err := tx.Model(&model.Inbound{}).Where("stable_id IN ? AND (last_traffic_reset_time IS NULL OR last_traffic_reset_time < ?)", batch, op.stamps[0].ResetAt).Count(&stale).Error; err != nil {
			return err
		}
		if stale != 0 {
			return ErrClientPolicyLedger
		}
	}
	for _, effect := range op.renewals {
		account, err := a.state.Journal.LookupAccount(effect.ClientID)
		if err != nil {
			return err
		}
		if account.Deleted {
			continue // The original UUID still requires tombstone/core proof below.
		}
		var rows []struct {
			ExpiryTime        sql.NullInt64
			TrafficExpiryTime sql.NullInt64
			ResetCount        sql.NullInt64
		}
		// Count is a consumed floor; expiry mirrors today's canonical desired
		// state. Completion observes both without replaying the old effects.
		if err := tx.Table("clients c").Select("c.expiry_time, ct.expiry_time AS traffic_expiry_time, ct.reset_count").
			Joins("LEFT JOIN client_traffics ct ON ct.email = c.email").
			Where("c.stable_id = ?", effect.ClientID).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 || !rows[0].ResetCount.Valid || rows[0].ResetCount.Int64 < int64(effect.AfterResetCount) || !rows[0].ExpiryTime.Valid || !rows[0].TrafficExpiryTime.Valid || rows[0].ExpiryTime.Int64 != rows[0].TrafficExpiryTime.Int64 {
			return ErrClientPolicyLedger
		}
	}
	for _, reset := range op.resets {
		var row model.ClientPolicyReset
		if err := tx.Where("client_id = ? AND request_id = ?", reset.ClientID, reset.RequestID).First(&row).Error; err != nil {
			return err
		}
		row.Id = 0
		if row != reset {
			return ErrClientPolicyLedger
		}
		account, err := a.state.Journal.LookupAccount(reset.ClientID)
		if err != nil {
			return err
		}
		if !account.Deleted && account.Policy.Version < uint64(reset.PolicyVersion) || !startupUsageCovers(&command.Usage{RawUpload: account.Usage.RawUpload, RawDownload: account.Usage.RawDownload, BilledBytes: account.Usage.BilledBytes, Remainder: account.Usage.Remainder}, reset) {
			return ErrClientPolicyLedger
		}
	}
	for _, id := range op.clients {
		account, err := a.state.Journal.LookupAccount(id)
		if err != nil {
			return err
		}
		policy, active := wanted[id]
		if account.Deleted {
			if active {
				return ErrClientPolicyLedger
			}
			var count int64
			if err := tx.Model(&model.ClientPolicyTombstone{}).Where("client_id = ?", id).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return ErrClientPolicyLedger
			}
		} else {
			if !active {
				return ErrClientPolicyLedger
			}
			if err := validateAuthorityDesiredPolicyTx(tx, policy); err != nil {
				return err
			}
		}
	}
	return nil
}

func startupUsageCovers(usage *command.Usage, reset model.ClientPolicyReset) bool {
	return usage != nil && usage.RawUpload >= uint64(reset.RawUpload) && usage.RawDownload >= uint64(reset.RawDownload) && (usage.BilledBytes > uint64(reset.BilledBytes) || usage.BilledBytes == uint64(reset.BilledBytes) && usage.Remainder >= uint64(reset.Remainder))
}

func (a *managedAuthority) verifyStartupCore(ctx context.Context, op *authorityStartupOperation, compiled map[string]*clientpolicy.PolicyConfig) error {
	floors := make(map[string]model.ClientPolicyReset, len(op.resets))
	for _, reset := range op.resets {
		floors[reset.ClientID] = reset
	}
	identity := a.state.Journal.Identity()
	binding := &command.AuthorityBinding{AuthorityId: identity.AuthorityID, Generation: identity.Generation, NodeId: a.controller.execution.boot.NodeID}
	// Authorization already enabled demand during activation. Repeating this
	// idempotent RPC verifies boot and binding immediately without long polling
	// or allocating any execution grant.
	if err := a.api.EnableAuthorityRequests(ctx, binding); err != nil {
		return err
	}
	for _, id := range op.clients {
		state, err := a.api.GetClient(ctx, id)
		wanted, active := compiled[id]
		if !active {
			if status.Code(err) == codes.NotFound {
				continue
			}
			if err != nil {
				return err
			}
			if state == nil || state.Reasons&uint32(clientpolicy.ReasonRevoked) == 0 {
				return ErrClientPolicyLedger
			}
			continue
		}
		if err != nil {
			return err
		}
		if state == nil || !proto.Equal(state.Policy, wanted) {
			return ErrClientPolicyLedger
		}
		if reset, ok := floors[id]; ok && !startupUsageCovers(state.Usage, reset) {
			return ErrClientPolicyLedger
		}
	}
	if err := a.api.EnableAuthorityRequests(ctx, binding); err != nil {
		return err
	}
	return a.validateStartupOwner(ctx)
}

// CompleteStartupOperations records only witnessed execution by the retained
// current process. Core queries run outside the serialized SQL transaction;
// source and desired state are checked again immediately before journaling.
func (a *managedAuthority) CompleteStartupOperations(ctx context.Context) error {
	if a == nil || ctx == nil {
		return ErrClientPolicyLedger
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.validateStartupOwner(ctx); err != nil {
		return err
	}
	if a.delegated() {
		page, err := a.state.Journal.ResetOperationPage("", 1)
		if err != nil {
			return err
		}
		if len(page) != 0 {
			return ErrClientPolicyLedger
		}
		return nil
	}
	config, err := a.config.Build()
	if err != nil {
		return err
	}
	wanted := make(map[string]clientpolicy.Policy, len(a.config.Policies))
	compiled := make(map[string]*clientpolicy.PolicyConfig, len(config.Policies))
	for _, policy := range a.config.Policies {
		wanted[policy.ClientID] = policy
	}
	for _, policy := range config.Policies {
		compiled[policy.ClientId] = policy
	}
	for after := ""; ; {
		if err := a.validateStartupOwner(ctx); err != nil {
			return err
		}
		page, err := a.state.Journal.ResetOperationPage(after, 128)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, header := range page {
			op, err := a.pendingStartupOperation(header.RequestID)
			if err != nil {
				return err
			}
			if op == nil {
				continue
			}
			validate := func(tx *gorm.DB) error {
				if err := a.validateStartupOwner(ctx); err != nil {
					return err
				}
				return a.validateStartupOperationTx(tx, op, wanted)
			}
			if err := runSerializedTxContextForDatabase(ctx, a.db, validate); err != nil {
				return err
			}
			if err := a.verifyStartupCore(ctx, op, compiled); err != nil {
				return err
			}
			if err := runSerializedTxContextForDatabase(ctx, a.db, func(tx *gorm.DB) error {
				if err := validate(tx); err != nil {
					return err
				}
				if err := a.validateStartupOwner(ctx); err != nil {
					return err
				}
				return a.state.Journal.CompleteResetOperation(policyauthority.ResetOperationCompletion{
					Identity: op.prepared.Identity, SourceID: op.prepared.SourceID,
					RequestID: op.prepared.RequestID, PreparationDigest: authorityResetSnapshotDigest(op.prepared.Snapshot),
				})
			}); err != nil {
				return err
			}
		}
		after = page[len(page)-1].RequestID
	}
}
