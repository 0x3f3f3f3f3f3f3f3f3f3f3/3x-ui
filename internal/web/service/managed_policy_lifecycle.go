package service

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
)

var managedCoordinatorOwner struct {
	sync.Mutex
	coordinator *managedPolicyCoordinator
}

func getManagedPolicyCoordinator(ctx context.Context, activate bool) (*managedPolicyCoordinator, error) {
	if ctx == nil {
		return nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := checkDatabaseRestoreRestart(nil); err != nil {
		return nil, err
	}
	managedCoordinatorOwner.Lock()
	defer managedCoordinatorOwner.Unlock()
	if c := managedCoordinatorOwner.coordinator; c != nil {
		c.mu.Lock()
		err := c.withCurrent(ctx, func(*gorm.DB) error { return nil })
		c.mu.Unlock()
		return c, err
	}
	db := database.GetDB()
	if !activate {
		var count int64
		if err := runSerializedTxContextForDatabase(ctx, db, func(tx *gorm.DB) error {
			return tx.Model(&model.ClientPolicyCoordinatorSource{}).Where("node_key = ?", managedCoordinatorSourceKey).Count(&count).Error
		}); err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, nil
		}
	}
	dir := filepath.Join(config.GetDBFolderPath(), "client-policy", "coordinator")
	c, err := openManagedPolicyCoordinator(ctx, db, dir)
	if err != nil {
		return nil, err
	}
	managedCoordinatorOwner.coordinator = c
	return c, nil
}

func ResumeManagedPolicyCoordinator(ctx context.Context) error {
	c, err := getManagedPolicyCoordinator(ctx, false)
	if err != nil || c == nil {
		return err
	}
	return c.ResumeNodes(ctx)
}

func StopManagedPolicyCoordinator(ctx context.Context) error {
	if ctx == nil {
		return ErrClientPolicyLedger
	}
	managedCoordinatorOwner.Lock()
	defer managedCoordinatorOwner.Unlock()
	c := managedCoordinatorOwner.coordinator
	if c == nil {
		return ctx.Err()
	}
	err := c.Close(ctx)
	if c.closed {
		managedCoordinatorOwner.coordinator = nil
	}
	return err
}
