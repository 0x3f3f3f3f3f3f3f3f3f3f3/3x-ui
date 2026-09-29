package service

import (
	"context"
	"slices"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
)

var managedPolicyOwners = struct {
	sync.Mutex
	byDB map[*gorm.DB]*managedPolicyOwner
}{byDB: make(map[*gorm.DB]*managedPolicyOwner)}

type managedPolicyOwner struct {
	controller *policyflow.Controller
	references int
}

type managedPolicyLease struct {
	controller *policyflow.Controller
	db         *gorm.DB
	once       sync.Once
}

func acquireManagedPolicy(db *gorm.DB) *managedPolicyLease {
	managedPolicyOwners.Lock()
	defer managedPolicyOwners.Unlock()
	owner := managedPolicyOwners.byDB[db]
	if owner == nil {
		owner = &managedPolicyOwner{controller: policyflow.NewController(database.NewClientUsageLedger(db), "local/managed-services")}
		managedPolicyOwners.byDB[db] = owner
	}
	owner.references++
	return &managedPolicyLease{controller: owner.controller, db: db}
}

func (lease *managedPolicyLease) Close() {
	lease.once.Do(func() {
		managedPolicyOwners.Lock()
		defer managedPolicyOwners.Unlock()
		owner := managedPolicyOwners.byDB[lease.db]
		owner.references--
		if owner.references == 0 {
			// Finish the retiring controller before another owner can claim its source.
			owner.controller.Close()
			delete(managedPolicyOwners.byDB, lease.db)
		}
	})
}

func loadManagedClientPolicies(ctx context.Context, db *gorm.DB, ids []string) (map[string]model.ClientPolicySettings, error) {
	slices.Sort(ids)
	ids = slices.Compact(ids)
	policies := make(map[string]model.ClientPolicySettings, len(ids))
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for _, part := range chunkStrings(ids, sqlInChunk) {
		var rows []model.ClientPolicySettings
		if err := db.WithContext(ctx).Where("policy_id IN ?", part).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Scope != "local" {
				return nil, ErrClientPolicyUnsupported
			}
			if row.UploadBps < 0 || row.UploadBps > clientpolicy.MaxRate || row.DownloadBps < 0 || row.DownloadBps > clientpolicy.MaxRate {
				return nil, clientpolicy.ErrInvalidRate
			}
			policies[row.PolicyID] = row
		}
	}
	return policies, nil
}
