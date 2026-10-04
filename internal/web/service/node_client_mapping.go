package service

import (
	"context"
	"errors"
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Expected per-client invalidation is distinct from SQL or authority failures.
var errClientMappingInactive = errors.New("client mapping does not authorize current policy")

func (*ClientPolicyNodeService) EnrollClientMapping(ctx context.Context, request panelruntime.NodeClientMappingRequest) (*panelruntime.NodeClientMappingResult, error) {
	return ownedNodeClientMapping(ctx, request, enrollOwnedNodeClientMapping)
}

func ownedNodeClientMapping(ctx context.Context, request panelruntime.NodeClientMappingRequest, operation func(context.Context, *managedAuthority, *gorm.DB, panelruntime.NodeAuthorityControlIdentity, panelruntime.NodeClientMappingRequest) (*panelruntime.NodeClientMappingResult, error)) (*panelruntime.NodeClientMappingResult, error) {
	if request.Validate() != nil {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	var result *panelruntime.NodeClientMappingResult
	err := withOwnedNodeAuthoritySQL(ctx, request.Binding, func(ctx context.Context, owner *managedAuthority, connection *gorm.DB, identity panelruntime.NodeAuthorityControlIdentity) error {
		var err error
		result, err = operation(ctx, owner, connection, identity, request)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func mappingDesiredPolicy(tx *gorm.DB, request panelruntime.NodeClientMappingRequest) (*clientpolicy.PolicyConfig, error) {
	var client model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id = ?", request.LocalClientID).First(&client).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var account model.ClientPolicyNodeAccount
			if err := tx.First(&account, "client_id = ?", request.LocalClientID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, errClientMappingInactive
				}
				return nil, err
			}
			if account.NodeID != request.Binding.NodeID || account.SourceID != request.Binding.ExpectedInstanceID {
				return nil, errClientMappingInactive
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&client, "stable_id = ?", account.ParentClientID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, errClientMappingInactive
				}
				return nil, err
			}
			// Preparation locks parent before account. Match that order and reread
			// the immutable identity after acquiring the parent lock.
			var locked model.ClientPolicyNodeAccount
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "client_id = ?", account.ClientID).Error; err != nil {
				return nil, err
			}
			if locked.ParentClientID != account.ParentClientID || locked.NodeID != account.NodeID || locked.SourceID != account.SourceID {
				return nil, ErrClientPolicyLedger
			}
			account = locked
			if client.Policy.EffectiveScope() != model.ClientPolicyScopeNode {
				return nil, errClientMappingInactive
			}
			if err := rejectDeletedClientPolicies(tx, []string{account.ParentClientID}); err != nil {
				if errors.Is(err, clientpolicy.ErrRevoked) {
					return nil, errClientMappingInactive
				}
				return nil, err
			}
			client.StableID, client.DesiredPolicyVersion, client.PolicyFingerprint = account.ClientID, account.DesiredPolicyVersion, account.PolicyFingerprint
		} else {
			return nil, err
		}
	}
	if client.DesiredPolicyVersion <= 0 || uint64(client.DesiredPolicyVersion) != request.LocalPolicyVersion {
		return nil, errClientMappingInactive
	}
	if err := rejectDeletedClientPolicies(tx, []string{request.LocalClientID}); err != nil {
		if errors.Is(err, clientpolicy.ErrRevoked) {
			return nil, errClientMappingInactive
		}
		return nil, err
	}
	resets, err := latestClientPolicyResets(tx, []string{request.LocalClientID})
	if err != nil {
		return nil, err
	}
	p, fingerprint, err := fingerprintClientPolicy(client, resets[request.LocalClientID])
	if err != nil {
		return nil, err
	}
	if fingerprint != client.PolicyFingerprint {
		return nil, errClientMappingInactive
	}
	return &clientpolicy.PolicyConfig{ClientId: p.ClientID, Version: uint64(client.DesiredPolicyVersion), Enabled: p.Enabled, MultiplierMicros: p.Multiplier, QuotaBytes: p.QuotaBytes, UploadBytesPerSecond: p.UploadRate, DownloadBytesPerSecond: p.DownloadRate, BurstBytes: p.BurstBytes, ExpiresAt: p.ExpiresAt, QuotaBaselineBytes: p.QuotaBaselineBytes, QuotaBaselineRemainder: p.QuotaBaselineRemainder}, nil
}

func mappingSQLFresh(tx *gorm.DB, request panelruntime.NodeClientMappingRequest) error {
	var count int64
	if err := tx.Model(&model.ClientPolicyReset{}).Where("client_id = ?", request.LocalClientID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrClientPolicyLedger
	}
	if err := tx.Model(&model.ClientPolicyTotal{}).Where("client_id = ? AND (raw_upload <> 0 OR raw_download <> 0 OR billed_bytes <> 0 OR uncertain_bytes <> 0)", request.LocalClientID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrClientPolicyLedger
	}
	if err := tx.Model(&model.ClientPolicyReceipt{}).Where("client_id = ? AND (instance_id <> ? OR raw_upload <> 0 OR raw_download <> 0 OR billed_bytes <> 0 OR remainder <> 0 OR uncertain_bytes <> 0 OR reserved_bytes <> 0 OR first_used_at <> 0 OR seed_upload <> 0 OR seed_download <> 0 OR seed_billed <> 0 OR revoked = ? OR deletion_absent = ?)", request.LocalClientID, request.Binding.ExpectedInstanceID, true, true).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrClientPolicyLedger
	}
	return nil
}

func mappingCorePolicy(ctx context.Context, owner *managedAuthority, request panelruntime.NodeClientMappingRequest, desired *clientpolicy.PolicyConfig, fresh bool) error {
	state, err := owner.api.GetClient(ctx, request.LocalClientID)
	if err != nil {
		return err
	}
	if state == nil || state.Policy == nil || state.Usage == nil || !proto.Equal(state.Policy, desired) || state.Reasons&uint32(clientpolicy.ReasonRevoked|clientpolicy.ReasonStorage) != 0 {
		return ErrManagedConfigStale
	}
	digest, err := panelruntime.EffectiveClientPolicyDigest(state.Policy)
	if err != nil || digest != request.ExpectedPolicyDigest {
		return ErrManagedConfigStale
	}
	if fresh && (state.AuthorityGrantHistory || !proto.Equal(state.Usage, &command.Usage{}) || state.UncertainBytes != 0 || state.FirstUsedAt != 0 || state.ActiveSessions != 0) {
		return ErrClientPolicyLedger
	}
	return nil
}

func mappingJournalCurrent(owner *managedAuthority, request panelruntime.NodeClientMappingRequest) error {
	account, err := owner.state.Journal.Account(request.LocalClientID)
	if err != nil {
		return err
	}
	if account.Deleted || account.Policy.Version != request.LocalPolicyVersion {
		return ErrClientPolicyLedger
	}
	return nil
}

func enrollOwnedNodeClientMapping(ctx context.Context, owner *managedAuthority, connection *gorm.DB, identity panelruntime.NodeAuthorityControlIdentity, request panelruntime.NodeClientMappingRequest) (*panelruntime.NodeClientMappingResult, error) {
	if !slices.Contains(owner.api.Capabilities().Capabilities, "client-authority-history-v1") {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	if err := mappingJournalCurrent(owner, request); err != nil {
		return nil, err
	}
	mapping := policyauthority.ClientMapping{Authority: policyauthority.Identity{AuthorityID: request.Binding.AuthorityID, Generation: request.Binding.Generation}, NodeAnchor: owner.state.Journal.Identity(), NodeID: request.Binding.NodeID, SourceID: identity.InstanceID, GlobalClientID: request.GlobalClientID, LocalClientID: request.LocalClientID, GlobalPolicyVersion: request.GlobalPolicyVersion, LocalPolicyVersion: request.LocalPolicyVersion, PolicyDigest: request.ExpectedPolicyDigest}
	stored, lookupErr := owner.state.Journal.LookupClientMapping(policyauthority.ClientMappingNode, mapping.SourceID, mapping.LocalClientID)
	fresh := errors.Is(lookupErr, policyauthority.ErrNotFound)
	if !fresh && (lookupErr != nil || stored != mapping) {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	// The outer admission already retains this database. Do not reacquire its
	// restore lock through the serialized writer while a restore drains readers.
	err := connection.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		desired, err := mappingDesiredPolicy(tx, request)
		if err != nil {
			return err
		}
		if fresh {
			if err := mappingSQLFresh(tx, request); err != nil {
				return err
			}
		}
		if err := mappingCorePolicy(ctx, owner, request, desired, fresh); err != nil {
			return err
		}
		if err := owner.validateStartupOwner(ctx); err != nil {
			return err
		}
		if err := owner.state.Journal.RecordClientMapping(policyauthority.ClientMappingNode, mapping); err != nil {
			return err
		}
		// A failed acknowledgement retains the original committed evidence.
		if err := mappingJournalCurrent(owner, request); err != nil {
			return err
		}
		return mappingCorePolicy(ctx, owner, request, desired, false)
	})
	if err != nil {
		return nil, err
	}
	result := &panelruntime.NodeClientMappingResult{NodeAuthorityControlIdentity: identity, Mapping: mapping}
	return result, result.Validate(request)
}
