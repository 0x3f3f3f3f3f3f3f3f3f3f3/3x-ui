package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

var ErrClientPolicyStateMissing = errors.New("previously activated client policy state is missing; restore its durable state before activation")

// A pending source is committed before creating the file, so interrupted creation keeps its identity.
// Only a source with no activated epoch or ledger cursor may create a missing store.
func EnsureLocalClientPolicyState(dir string) (*conf.ClientPolicyConfig, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: private state directory: %w", clientpolicy.ErrStorage, err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: state directory must be private and cannot be a symbolic link", clientpolicy.ErrStorage)
	}
	var source model.ClientPolicySource
	err = runSerializedTx(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_key = ?", "local").First(&source).Error
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		id, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		source = model.ClientPolicySource{NodeKey: "local", InstanceID: id.String()}
		return tx.Create(&source).Error
	})
	if err != nil {
		return nil, err
	}
	state := filepath.Join(dir, "state.db")
	err = runSerializedTx(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_key = ?", "local").First(&source).Error; err != nil {
			return err
		}
		if !validPolicySourceKey(source.InstanceID) || source.Epoch < 0 || source.Sequence < 0 {
			return ErrClientPolicyLedger
		}
		info, err := os.Lstat(state)
		if errors.Is(err, os.ErrNotExist) {
			if source.Epoch != 0 || source.Sequence != 0 {
				return ErrClientPolicyStateMissing
			}
			return clientpolicy.CreateStore(state, source.InstanceID)
		}
		if err != nil {
			return fmt.Errorf("%w: inspect state: %w", clientpolicy.ErrStorage, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%w: state must be a private regular file", clientpolicy.ErrStorage)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &conf.ClientPolicyConfig{StateFile: state, InstanceID: source.InstanceID}, nil
}
