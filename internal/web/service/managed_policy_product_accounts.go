package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

type ManagedPolicyAccountPageRequest struct {
	ParentClientID string `json:"parentClientId"`
	AfterNode      string `json:"afterNode"`
	Limit          int    `json:"limit"`
}

type ManagedPolicyAccountStatus struct {
	ClientID       string                  `json:"clientId"`
	Scope          string                  `json:"scope" validate:"oneof=node global"`
	NodeID         string                  `json:"nodeId"`
	PolicyVersion  string                  `json:"policyVersion"`
	Enrolled       bool                    `json:"enrolled"`
	PolicyPending  bool                    `json:"policyPending"`
	Deleted        bool                    `json:"deleted"`
	QuotaUnlimited bool                    `json:"quotaUnlimited"`
	QuotaBytes     string                  `json:"quotaBytes"`
	Usage          xray.ClientPolicyUsage  `json:"usage"`
	WindowUsed     string                  `json:"windowUsed"`
	Remaining      *string                 `json:"remaining"`
	Budget         xray.ClientPolicyBudget `json:"budget"`
}

type ManagedPolicyAccountPage struct {
	Scope             string                       `json:"scope" validate:"oneof=node global"`
	PendingEnrollment bool                         `json:"pendingEnrollment"`
	Accounts          []ManagedPolicyAccountStatus `json:"accounts"`
	NextNode          string                       `json:"nextNode"`
}

func (request ManagedPolicyAccountPageRequest) Validate() error {
	parentID, err := uuid.Parse(request.ParentClientID)
	if err != nil || parentID == uuid.Nil || parentID.String() != request.ParentClientID || request.Limit < 1 || request.Limit > 128 || request.AfterNode != "" && !validPolicySourceKey(request.AfterNode) {
		return ErrClientPolicyLedger
	}
	return nil
}

func (s *ManagedPolicyCoordinatorService) Accounts(ctx context.Context, request ManagedPolicyAccountPageRequest) (*ManagedPolicyAccountPage, error) {
	if ctx == nil || request.Validate() != nil {
		return nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := getManagedPolicyCoordinator(ctx, false)
	if err != nil {
		return nil, err
	}
	result := &ManagedPolicyAccountPage{Accounts: make([]ManagedPolicyAccountStatus, 0, request.Limit)}
	if c == nil {
		err = runSerializedTxContextForDatabase(ctx, database.GetDB(), func(tx *gorm.DB) error {
			var parent model.ClientRecord
			if err := tx.First(&parent, "stable_id = ?", request.ParentClientID).Error; err != nil {
				return err
			}
			result.Scope = string(parent.Policy.EffectiveScope())
			result.PendingEnrollment = true
			return nil
		})
	} else {
		c.mu.Lock()
		defer c.mu.Unlock()
		err = c.withCurrent(ctx, func(tx *gorm.DB) error {
			var parent model.ClientRecord
			parentErr := tx.First(&parent, "stable_id = ?", request.ParentClientID).Error
			if parentErr != nil && !errors.Is(parentErr, gorm.ErrRecordNotFound) {
				return parentErr
			}
			result.Scope = string(parent.Policy.EffectiveScope())
			page, err := c.state.Journal.ManagedAccountPage(request.ParentClientID, request.AfterNode, request.Limit)
			if err != nil {
				return err
			}
			if parentErr != nil {
				// A missing SQL row cannot authorize a live account. Historical
				// membership remains readable only after original revocation.
				first, err := c.state.Journal.ManagedAccountPage(request.ParentClientID, "", 1)
				if err != nil || len(first) != 1 || !first[0].Account.Deleted {
					return ErrClientPolicyLedger
				}
				result.Scope = first[0].Origin.Scope
			}
			result.PendingEnrollment = len(page) == 0 && request.AfterNode == ""
			var connections []model.ClientPolicyCoordinatorNode
			if err := tx.Order("node_id").Limit(1001).Find(&connections).Error; err != nil {
				return err
			}
			if len(connections) > 1000 {
				return ErrClientPolicyLedger
			}
			for _, snapshot := range page {
				origin, account := snapshot.Origin, snapshot.Account
				if parentErr != nil && !account.Deleted {
					return ErrClientPolicyLedger
				}
				row := ManagedPolicyAccountStatus{ClientID: origin.ClientID, Scope: origin.Scope, NodeID: origin.NodeID, PolicyVersion: strconv.FormatUint(account.Policy.Version, 10), Deleted: account.Deleted, QuotaUnlimited: account.Policy.QuotaUnlimited, QuotaBytes: strconv.FormatUint(account.Policy.QuotaBytes, 10), Usage: formatClientPolicyUsage(int64(account.Usage.RawUpload), int64(account.Usage.RawDownload), int64(account.Usage.BilledBytes), int64(account.Usage.Remainder), int64(account.FrozenBilled)), WindowUsed: formatClientPolicyBilled(int64(account.WindowUsed), int64(account.WindowRemainder)), Budget: *formatClientPolicyBudget(&account)}
				withoutHeld := account
				withoutHeld.HeldCapacity, withoutHeld.HeldRemainder = 0, 0
				row.Remaining = formatClientPolicyBudget(&withoutHeld).Unallocated
				if account.Deleted {
					zero := "0"
					row.Remaining, row.Budget.Unallocated = &zero, &zero
				}
				if !account.Deleted {
					policy, policyErr := mappingDesiredPolicy(tx, panelruntime.NodeClientMappingRequest{LocalClientID: origin.ClientID, LocalPolicyVersion: account.Policy.Version, Binding: panelruntime.NodeAuthorityControlBinding{NodeID: origin.NodeID, ExpectedInstanceID: origin.SourceID}})
					if policyErr != nil {
						if !errors.Is(policyErr, errClientMappingInactive) {
							return policyErr
						}
						row.PolicyPending = true
					} else {
						digest, err := panelruntime.EffectiveClientPolicyDigest(policy)
						if err != nil {
							return err
						}
						row.PolicyPending = string(parent.Policy.EffectiveScope()) != origin.Scope || digest != origin.PolicyDigest
					}
				}
				candidates := connections
				if origin.Scope == "node" {
					candidates = []model.ClientPolicyCoordinatorNode{{NodeID: origin.NodeID, SourceID: origin.SourceID}}
				}
				for _, candidate := range candidates {
					proof, err := c.state.Journal.LookupClientMappingAccount(policyauthority.ClientMappingCoordinator, candidate.SourceID, origin.ClientID)
					if errors.Is(err, policyauthority.ErrNotFound) {
						continue
					}
					if err != nil {
						return err
					}
					if proof.NodeID != candidate.NodeID {
						return policyauthority.ErrIdentity
					}
					row.Enrolled = true
				}
				result.Scope = origin.Scope
				result.Accounts = append(result.Accounts, row)
				if origin.Scope == "node" && len(page) == request.Limit {
					result.NextNode = origin.NodeID
				}
				result.PendingEnrollment = result.PendingEnrollment || !row.Enrolled
			}
			return nil
		})
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}
