package service

import (
	"context"
	"errors"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Acquire the managed source before parent/account locks, matching preparation
// order. Settlement still records authentic old receipts after source rejection.
func validateManagedCoordinatorSourceTx(tx *gorm.DB, journal *policyauthority.Journal, client string) error {
	if _, err := journal.ManagedAccountOrigin(client); errors.Is(err, policyauthority.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	records, err := journal.MigrationPage("sources", "", "", 2)
	if err != nil {
		return err
	}
	if len(records) != 1 || string(records[0].Value) != `{"role":"managed-coordinator","schema":1}` {
		return policyauthority.ErrIdentity
	}
	var source model.ClientPolicyCoordinatorSource
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "node_key = ?", managedCoordinatorSourceKey).Error; err != nil {
		return err
	}
	if source.InstanceID != records[0].Key || !source.Activated {
		return policyauthority.ErrIdentity
	}
	return nil
}

func validateManagedAuthorityOriginTx(tx *gorm.DB, journal *policyauthority.Journal, account policyauthority.Account) error {
	origin, err := journal.ManagedAccountOrigin(account.Seed.ClientID)
	if errors.Is(err, policyauthority.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if account.Deleted {
		return nil
	}
	if origin.Scope == "global" {
		return nil
	} // UUID and parent are identical in the original origin.
	var node model.ClientPolicyNodeAccount
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&node, "client_id = ?", origin.ClientID).Error; err != nil {
		return err
	}
	if node.ParentClientID != origin.ParentClientID || node.NodeID != origin.NodeID || node.SourceID != origin.SourceID {
		return ErrClientPolicyLedger
	}
	var ordinary int64
	if err := tx.Model(&model.ClientRecord{}).Where("stable_id = ?", origin.ClientID).Count(&ordinary).Error; err != nil {
		return err
	}
	if ordinary != 0 {
		return ErrClientPolicyLedger
	}
	return nil
}

func validateManagedAuthorityPolicyTx(tx *gorm.DB, journal *policyauthority.Journal, binding policyauthority.Binding) error {
	origin, err := journal.ManagedAccountOrigin(binding.ClientID)
	if errors.Is(err, policyauthority.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if origin.InitialPolicyVersion != binding.PolicyVersion || origin.Scope == "node" && (origin.NodeID != binding.NodeBoot.NodeID || origin.SourceID != binding.NodeBoot.SourceID) {
		return ErrClientPolicyLedger
	}
	var parent model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "stable_id = ?", origin.ParentClientID).Error; err != nil {
		return err
	}
	if string(parent.Policy.EffectiveScope()) != origin.Scope {
		return errClientMappingInactive
	}
	request := panelruntime.NodeClientMappingRequest{LocalClientID: binding.ClientID, LocalPolicyVersion: binding.PolicyVersion, Binding: panelruntime.NodeAuthorityControlBinding{NodeID: binding.NodeBoot.NodeID, ExpectedInstanceID: binding.NodeBoot.SourceID}}
	policy, err := mappingDesiredPolicy(tx, request)
	if err != nil {
		return err
	}
	digest, err := panelruntime.EffectiveClientPolicyDigest(policy)
	if err != nil {
		return err
	}
	if digest != origin.PolicyDigest {
		return errClientMappingInactive
	}
	return nil
}

func (c *managedPolicyCoordinator) PrepareAccount(ctx context.Context, parentID string, member managedAuthorityMember) (policyauthority.ManagedAccountOrigin, *clientpolicy.PolicyConfig, error) {
	return c.prepareAccount(ctx, parentID, member, nil)
}

func (c *managedPolicyCoordinator) prepareAccount(ctx context.Context, parentID string, member managedAuthorityMember, preflight func(*gorm.DB) error) (policyauthority.ManagedAccountOrigin, *clientpolicy.PolicyConfig, error) {
	var origin policyauthority.ManagedAccountOrigin
	if c == nil || ctx == nil || !validPolicySourceKey(member.NodeID) || !validPolicySourceKey(member.SourceID) {
		return origin, nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Local preparation, restore and source admission use this same lifecycle
	// owner. Hold it before the coordinator/SQL locks and never across peer RPC.
	if !lock.TryLock() {
		return origin, nil, ErrClientPolicyLedger
	}
	defer lock.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return origin, nil, ErrClientPolicyLedger
	}
	var parent model.ClientRecord
	err := c.withCurrent(ctx, func(tx *gorm.DB) error {
		if preflight != nil {
			if err := preflight(tx); err != nil {
				return err
			}
		}
		return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "stable_id = ?", parentID).Error
	})
	if err != nil {
		return origin, nil, err
	}
	clientID := parentID
	if parent.Policy.EffectiveScope() == model.ClientPolicyScopeNode {
		if err := c.restoreNodeAccount(ctx, parentID, member); err != nil {
			return origin, nil, err
		}
		account, _, err := prepareNodeClientPolicyContextForDatabase(ctx, c.db, parentID, member.NodeID, member.SourceID)
		if err != nil {
			return origin, nil, err
		}
		clientID = account.ClientID
	} else {
		if _, err := prepareClientPoliciesContextForDatabase(ctx, c.db, []string{parentID}, nil); err != nil {
			return origin, nil, err
		}
	}
	// Persist SQL identity/version before committing original evidence. A lost
	// final SQL acknowledgement can then retry the same canonical UUID.
	var policy *clientpolicy.PolicyConfig
	err = c.withCurrent(ctx, func(tx *gorm.DB) error {
		if preflight != nil {
			if err := preflight(tx); err != nil {
				return err
			}
		}
		var current model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "stable_id = ?", parentID).Error; err != nil {
			return err
		}
		if current.Policy.EffectiveScope() != parent.Policy.EffectiveScope() {
			return ErrManagedConfigStale
		}
		version := current.DesiredPolicyVersion
		if clientID != parentID {
			var account model.ClientPolicyNodeAccount
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, "client_id = ?", clientID).Error; err != nil {
				return err
			}
			if account.ParentClientID != parentID || account.NodeID != member.NodeID || account.SourceID != member.SourceID {
				return ErrClientPolicyLedger
			}
			version = account.DesiredPolicyVersion
		}
		request := panelruntime.NodeClientMappingRequest{LocalClientID: clientID, LocalPolicyVersion: uint64(version), Binding: panelruntime.NodeAuthorityControlBinding{NodeID: member.NodeID, ExpectedInstanceID: member.SourceID}}
		var err error
		policy, err = mappingDesiredPolicy(tx, request)
		if err != nil {
			return err
		}
		digest, err := panelruntime.EffectiveClientPolicyDigest(policy)
		if err != nil {
			return err
		}
		origin = policyauthority.ManagedAccountOrigin{ClientID: clientID, ParentClientID: parentID, Scope: string(current.Policy.EffectiveScope()), InitialPolicyVersion: policy.Version, PolicyDigest: digest}
		if clientID != parentID {
			origin.NodeID, origin.SourceID = member.NodeID, member.SourceID
		}
		retained, err := c.state.Journal.ManagedAccountOrigin(clientID)
		if errors.Is(err, policyauthority.ErrNotFound) {
			if err := checkManagedOriginalLocalAdmission(ctx, c.db, tx, parentID, clientID); err != nil {
				return err
			}
			var historical int64
			for _, table := range []string{"client_traffics", "node_client_traffics", "client_global_traffics"} {
				if err := tx.Table(table).Where("email = ? AND (up <> 0 OR down <> 0)", current.Email).Count(&historical).Error; err != nil {
					return err
				}
				if historical != 0 {
					return ErrClientPolicyLedger
				}
			}
			for _, id := range []string{parentID, clientID} {
				fresh := request
				fresh.LocalClientID = id
				if err := mappingSQLFresh(tx, fresh); err != nil {
					return err
				}
			}
		} else if err != nil {
			return err
		} else if retained != origin {
			return ErrClientPolicyLedger
		}
		effective := clientpolicy.Policy{ClientID: policy.ClientId, Version: policy.Version, Enabled: policy.Enabled, Multiplier: policy.MultiplierMicros, QuotaBytes: policy.QuotaBytes, UploadRate: policy.UploadBytesPerSecond, DownloadRate: policy.DownloadBytesPerSecond, BurstBytes: policy.BurstBytes, ExpiresAt: policy.ExpiresAt, QuotaBaselineBytes: policy.QuotaBaselineBytes, QuotaBaselineRemainder: policy.QuotaBaselineRemainder}
		seed := policyauthority.Seed{ClientID: clientID, Policy: authorityPolicy(effective, "initial:"+clientID)}
		if err := c.state.Journal.AddManagedAccount(seed, origin); err != nil {
			return err
		}
		return projectClientPolicyAuthorityTx(tx, c.state.Journal, clientID)
	})
	if err != nil {
		return policyauthority.ManagedAccountOrigin{}, nil, err
	}
	return origin, policy, nil
}

func (c *managedPolicyCoordinator) restoreNodeAccount(ctx context.Context, parentID string, member managedAuthorityMember) error {
	origin, err := c.state.Journal.LookupManagedNodeOrigin(parentID, member.NodeID, member.SourceID)
	if errors.Is(err, policyauthority.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.withCurrent(ctx, func(tx *gorm.DB) error {
		var parent model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "stable_id = ?", origin.ParentClientID).Error; err != nil {
			return err
		}
		if parent.Policy.EffectiveScope() != model.ClientPolicyScopeNode {
			return ErrManagedConfigStale
		}
		if err := rejectDeletedClientPolicies(tx, []string{origin.ParentClientID, origin.ClientID}); err != nil {
			return err
		}
		account, err := c.state.Journal.Account(origin.ClientID)
		if err != nil {
			return err
		}
		if account.Deleted || account.Policy != account.Seed.Policy || account.Policy.Version != origin.InitialPolicyVersion {
			return ErrClientPolicyLedger
		}
		var retained model.ClientPolicyNodeAccount
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&retained, "client_id = ?", origin.ClientID).Error
		if err == nil {
			if retained.ParentClientID != origin.ParentClientID || retained.NodeID != origin.NodeID || retained.SourceID != origin.SourceID {
				return ErrClientPolicyLedger
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var conflicts int64
		if err := tx.Model(&model.ClientPolicyNodeAccount{}).Where("parent_client_id = ? AND (node_id = ? OR source_id = ?)", origin.ParentClientID, origin.NodeID, origin.SourceID).Count(&conflicts).Error; err != nil {
			return err
		}
		if conflicts != 0 {
			return ErrClientPolicyLedger
		}
		if err := tx.Model(&model.ClientRecord{}).Where("stable_id = ?", origin.ClientID).Count(&conflicts).Error; err != nil {
			return err
		}
		if conflicts != 0 {
			return ErrClientPolicyLedger
		}
		resets, err := latestClientPolicyResets(tx, []string{origin.ClientID})
		if err != nil {
			return err
		}
		parent.StableID, parent.DesiredPolicyVersion = origin.ClientID, int64(origin.InitialPolicyVersion)
		p, fingerprint, err := fingerprintClientPolicy(parent, resets[origin.ClientID])
		if err != nil {
			return err
		}
		p.Version = origin.InitialPolicyVersion
		config := &clientpolicy.PolicyConfig{ClientId: p.ClientID, Version: p.Version, Enabled: p.Enabled, MultiplierMicros: p.Multiplier, QuotaBytes: p.QuotaBytes, UploadBytesPerSecond: p.UploadRate, DownloadBytesPerSecond: p.DownloadRate, BurstBytes: p.BurstBytes, ExpiresAt: p.ExpiresAt, QuotaBaselineBytes: p.QuotaBaselineBytes, QuotaBaselineRemainder: p.QuotaBaselineRemainder}
		digest, err := panelruntime.EffectiveClientPolicyDigest(config)
		if err != nil {
			return err
		}
		if digest != origin.PolicyDigest {
			return ErrManagedConfigStale
		}
		retained = model.ClientPolicyNodeAccount{ClientID: origin.ClientID, ParentClientID: origin.ParentClientID, NodeID: origin.NodeID, SourceID: origin.SourceID, DesiredPolicyVersion: int64(origin.InitialPolicyVersion), PolicyFingerprint: fingerprint}
		return tx.Create(&retained).Error
	})
}
