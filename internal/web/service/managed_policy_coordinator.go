package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const managedCoordinatorSourceKey = "managed-coordinator"

type managedPolicyCoordinator struct {
	mu                       sync.Mutex
	connectionMu             sync.Mutex
	db                       *gorm.DB
	dir                      string
	state                    *durableAuthorityState
	controllers              map[string]*authorityController
	discovered               map[string]managedNodeDiscovery
	closed                   bool
	reconcileMu              sync.Mutex
	reconcileCancel          context.CancelFunc
	reconcileDone            chan struct{}
	deletionParentCursor     string
	deletionNodeCursor       string
	deletionControllerCursor string
}

func openManagedPolicyCoordinator(ctx context.Context, expected *gorm.DB, dir string) (*managedPolicyCoordinator, error) {
	if ctx == nil || expected == nil || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, ErrClientPolicyLedger
	}
	var source model.ClientPolicyCoordinatorSource
	err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := checkAuthorityDirectory(dir); err != nil {
			return err
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "node_key = ?", managedCoordinatorSourceKey).Error
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		for _, file := range []string{"authority.json", "journal.db"} {
			if _, err := os.Lstat(filepath.Join(dir, file)); !errors.Is(err, os.ErrNotExist) {
				return ErrAuthorityNotInitialized
			}
		}
		source = model.ClientPolicyCoordinatorSource{NodeKey: managedCoordinatorSourceKey, InstanceID: uuid.NewString()}
		return tx.Create(&source).Error
	})
	if err != nil {
		return nil, err
	}
	var state *durableAuthorityState
	err = runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "node_key = ?", managedCoordinatorSourceKey).Error; err != nil {
			return err
		}
		if !validPolicySourceKey(source.InstanceID) {
			return ErrClientPolicyLedger
		}
		var err error
		if !source.Activated {
			snapshot := authorityMigrationSnapshot{Records: []policyauthority.MigrationRecord{{Kind: "sources", Key: source.InstanceID, Value: []byte(`{"role":"managed-coordinator","schema":1}`)}}}
			state, err = initializeAuthorityState(dir, source.InstanceID, snapshot)
		} else {
			state, err = resumeAuthorityState(dir, source.InstanceID)
		}
		if err != nil {
			return err
		}
		if state.Role != (panelruntime.NodeExecutionRole{Mode: panelruntime.NodeExecutionLocal}) {
			return policyauthority.ErrIdentity
		}
		record, err := state.Journal.MigrationRecord("sources", source.InstanceID)
		if err != nil {
			return err
		}
		if string(record.Value) != `{"role":"managed-coordinator","schema":1}` {
			return policyauthority.ErrIdentity
		}
		if err := state.Journal.ActivateManagedCoordinator(source.InstanceID); err != nil {
			return err
		}
		return tx.Model(&model.ClientPolicyCoordinatorSource{}).Where("instance_id = ? AND node_key = ?", source.InstanceID, managedCoordinatorSourceKey).Update("activated", true).Error
	})
	if err != nil {
		if state != nil {
			_ = state.Journal.Close()
		}
		return nil, err
	}
	return &managedPolicyCoordinator{db: expected, dir: dir, state: state, controllers: make(map[string]*authorityController), discovered: make(map[string]managedNodeDiscovery)}, nil
}

// The caller holds the coordinator lock. SQL replacement and restore are
// checked again on every durable operation, not only when opening the owner.
func (c *managedPolicyCoordinator) withCurrent(ctx context.Context, operation func(*gorm.DB) error) error {
	if c == nil || ctx == nil || c.closed || c.state == nil || c.state.Journal == nil {
		return ErrClientPolicyLedger
	}
	return runSerializedTxContextForDatabase(ctx, c.db, func(tx *gorm.DB) error {
		var source model.ClientPolicyCoordinatorSource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "node_key = ?", managedCoordinatorSourceKey).Error; err != nil {
			return err
		}
		if source.InstanceID != c.state.SourceID || !source.Activated {
			return policyauthority.ErrIdentity
		}
		if err := validateManagedCoordinatorFiles(c.dir, c.state); err != nil {
			return err
		}
		return operation(tx)
	})
}

func validateManagedCoordinatorFiles(dir string, state *durableAuthorityState) error {
	if state == nil || state.Journal == nil {
		return ErrClientPolicyLedger
	}
	if err := checkAuthorityDirectory(dir); err != nil {
		return err
	}
	manifest, err := readAuthorityManifest(dir)
	if err != nil {
		return err
	}
	if manifest.Phase != "committed" || manifest.SourceID != state.SourceID || manifest.Identity != state.Journal.Identity() || manifest.role() != state.Role {
		return policyauthority.ErrIdentity
	}
	if err := state.Journal.CheckMigrationSource(manifest.SourceID, manifest.SnapshotDigest); err != nil {
		return err
	}
	return checkAuthorityExecutionRole(state.Journal, manifest)
}

func (c *managedPolicyCoordinator) Close(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.stopReconciler(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	keys := make([]string, 0, len(c.controllers))
	for key := range c.controllers {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var result error
	for _, key := range keys {
		controller := c.controllers[key]
		if err := controller.join(ctx); err != nil {
			return errors.Join(result, err)
		}
		result = errors.Join(result, controller.Stop(ctx))
	}
	if err := c.state.Journal.Close(); err != nil {
		return errors.Join(result, err)
	}
	c.closed = true
	return result
}
