package service

import (
	"sync"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
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
