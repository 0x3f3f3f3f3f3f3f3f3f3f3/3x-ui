package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

type ManagedPolicyContributionRequest struct {
	ParentClientID string `json:"parentClientId"`
	ClientID       string `json:"clientId"`
	AfterGrant     string `json:"afterGrant"`
	Limit          int    `json:"limit"`
}
type ManagedPolicyContribution struct {
	NodeID         string                 `json:"nodeId"`
	SourceID       string                 `json:"sourceId"`
	BootID         string                 `json:"bootId"`
	GrantID        string                 `json:"grantId"`
	GrantSequence  string                 `json:"grantSequence"`
	ReportSequence string                 `json:"reportSequence"`
	Sealed         bool                   `json:"sealed"`
	Usage          xray.ClientPolicyUsage `json:"usage"`
}
type ManagedPolicyContributionPage struct {
	Contributions []ManagedPolicyContribution `json:"contributions"`
	NextGrant     string                      `json:"nextGrant"`
}

func (r ManagedPolicyContributionRequest) Validate() error {
	for _, value := range []string{r.ParentClientID, r.ClientID} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			return ErrClientPolicyLedger
		}
	}
	if r.Limit < 1 || r.Limit > 16 || len(r.AfterGrant) > 512 {
		return ErrClientPolicyLedger
	}
	return nil
}

// Each call examines at most16 original grants. The cursor advances even when
// those grants belong to another account; SQL and seeds supply no usage here.
func (s *ManagedPolicyCoordinatorService) Contributions(ctx context.Context, r ManagedPolicyContributionRequest) (*ManagedPolicyContributionPage, error) {
	if ctx == nil || r.Validate() != nil {
		return nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := getManagedPolicyCoordinator(ctx, false)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ErrAuthorityNotInitialized
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	result := &ManagedPolicyContributionPage{Contributions: make([]ManagedPolicyContribution, 0, r.Limit)}
	err = c.withCurrent(ctx, func(_ *gorm.DB) error {
		origin, err := c.state.Journal.ManagedAccountOrigin(r.ClientID)
		if err != nil || origin.ParentClientID != r.ParentClientID {
			return errors.Join(ErrClientPolicyLedger, err)
		}
		grants, err := c.state.Journal.GrantPage(r.AfterGrant, r.Limit)
		if err != nil {
			return err
		}
		for _, grant := range grants {
			binding := grant.Request.Binding
			if binding.ClientID != r.ClientID {
				continue
			}
			if origin.Scope == "node" && (binding.NodeBoot.NodeID != origin.NodeID || binding.NodeBoot.SourceID != origin.SourceID) {
				return policyauthority.ErrIdentity
			}
			proof, err := c.state.Journal.LookupClientMappingAccount(policyauthority.ClientMappingCoordinator, binding.NodeBoot.SourceID, r.ClientID)
			if err != nil || proof.NodeID != binding.NodeBoot.NodeID {
				return errors.Join(policyauthority.ErrIdentity, err)
			}
			result.Contributions = append(result.Contributions, ManagedPolicyContribution{NodeID: binding.NodeBoot.NodeID, SourceID: binding.NodeBoot.SourceID, BootID: binding.NodeBoot.BootID, GrantID: grant.GrantID, GrantSequence: strconv.FormatUint(grant.Sequence, 10), ReportSequence: strconv.FormatUint(grant.ReportSequence, 10), Sealed: grant.Sealed, Usage: formatClientPolicyUsage(int64(grant.Usage.RawUpload), int64(grant.Usage.RawDownload), int64(grant.Usage.BilledBytes), int64(grant.Usage.Remainder), 0)})
		}
		if len(grants) == r.Limit {
			result.NextGrant = grants[len(grants)-1].GrantID
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
