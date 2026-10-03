package service

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
)

type ClientPolicyNodeService struct{}

func (*ClientPolicyNodeService) DiscoverAuthority(ctx context.Context, request panelruntime.AuthorityDiscoveryRequest) (*panelruntime.NodeAuthorityDiscovery, error) {
	if ctx == nil || request.Validate() != nil {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !lock.TryLock() {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	defer lock.Unlock()
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return nil, err
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	if owner == nil || !owner.mu.TryLock() {
		return nil, panelruntime.ErrNodeAuthorityDiscovery
	}
	defer owner.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var result *panelruntime.NodeAuthorityDiscovery
	err := database.WithCurrentDB(owner.db, func(current *gorm.DB) error {
		return database.WithConnection(current.WithContext(ctx), func(_ *gorm.DB) error {
			if err := owner.validateStartupOwner(ctx); err != nil {
				return err
			}
			capabilities := owner.api.Capabilities()
			if request.ExpectedInstanceID != "" && (request.ExpectedInstanceID != capabilities.InstanceId || request.ExpectedBootID != capabilities.BootId) {
				return panelruntime.ErrNodeAuthorityDiscovery
			}
			challenge, err := owner.api.AuthorityChallenge(ctx)
			if err != nil {
				return err
			}
			result = &panelruntime.NodeAuthorityDiscovery{Capabilities: capabilities, Challenge: challenge}
			if err := result.Validate(request); err != nil {
				return err
			}
			return owner.validateStartupOwner(ctx)
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
