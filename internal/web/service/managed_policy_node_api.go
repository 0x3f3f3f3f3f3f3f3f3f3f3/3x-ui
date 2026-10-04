package service

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Creation and renewal require current inventory admission. The embedded pinned
// Get/Pause/Seal operations can still reconcile authentic old allocations.
type managedNodeDemandAPI struct {
	authorityDemandAPI
	db           *gorm.DB
	journal      *policyauthority.Journal
	state        *durableAuthorityState
	dir          string
	node         model.Node
	clientID     string
	mappingCount int
}

func (a *managedNodeDemandAPI) admit(ctx context.Context) error {
	if a == nil || ctx == nil {
		return ErrClientPolicyLedger
	}
	return runSerializedTxContextForDatabase(ctx, a.db, func(tx *gorm.DB) error {
		if a.state == nil || a.state.Journal != a.journal {
			return ErrClientPolicyLedger
		}
		if err := validateManagedCoordinatorFiles(a.dir, a.state); err != nil {
			return err
		}
		if err := validateManagedCoordinatorSourceTx(tx, a.journal, a.clientID); err != nil {
			return err
		}
		var current model.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, a.node.Id).Error; err != nil {
			return err
		}
		if !current.Enable || !managedNodeConnectionUnchanged(a.node, current) {
			return ErrManagedConfigStale
		}
		return nil
	})
}

func (a *managedNodeDemandAPI) BindAuthority(ctx context.Context, b *command.AuthorityBinding) error {
	if err := a.admit(ctx); err != nil {
		return err
	}
	return a.authorityDemandAPI.BindAuthority(ctx, b)
}
func (a *managedNodeDemandAPI) EnableAuthorityRequests(ctx context.Context, b *command.AuthorityBinding) error {
	if err := a.admit(ctx); err != nil {
		return err
	}
	return a.authorityDemandAPI.EnableAuthorityRequests(ctx, b)
}
func (a *managedNodeDemandAPI) AuthorityChallenge(ctx context.Context) (*command.AuthorityChallenge, error) {
	if err := a.admit(ctx); err != nil {
		return nil, err
	}
	return a.authorityDemandAPI.AuthorityChallenge(ctx)
}
func (a *managedNodeDemandAPI) ReadAuthorityRequests(ctx context.Context, b *command.AuthorityBinding, n uint32) (*command.AuthorityRequests, error) {
	if err := a.admit(ctx); err != nil {
		return nil, err
	}
	return a.authorityDemandAPI.ReadAuthorityRequests(ctx, b, n)
}
func (a *managedNodeDemandAPI) InstallAuthorityGrant(ctx context.Context, g *command.ExecutionGrant) (*command.ExecutionGrantState, error) {
	if err := a.admit(ctx); err != nil {
		return nil, err
	}
	return a.authorityDemandAPI.InstallAuthorityGrant(ctx, g)
}
func (a *managedNodeDemandAPI) RenewAuthorityGrant(ctx context.Context, r *command.AuthorityRenewalRequest) error {
	if err := a.admit(ctx); err != nil {
		return err
	}
	return a.authorityDemandAPI.RenewAuthorityGrant(ctx, r)
}
