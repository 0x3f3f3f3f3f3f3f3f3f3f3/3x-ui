package database

import (
	"errors"

	"gorm.io/gorm"
)

var ErrDatabaseReplaced = errors.New("database was closed or replaced before the operation could commit; retry from the current database")

// WithCurrentDB keeps replacement/close from crossing fn, including a SQL
// transaction's final commit. GetDB remains an atomic read, so SQL helpers
// inside fn do not acquire a recursive lifecycle read lock.
func WithCurrentDB(expected *gorm.DB, fn func(*gorm.DB) error) error {
	databaseLifecycle.RLock()
	defer databaseLifecycle.RUnlock()
	if expected == nil || !databaseReady.Load() || GetDB() != expected {
		return ErrDatabaseReplaced
	}
	return fn(expected)
}
