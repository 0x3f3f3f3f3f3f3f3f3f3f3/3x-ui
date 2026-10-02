package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"
)

var ErrRestoreInProgress = errors.New("database restore is already in progress; retry after it finishes")
var ErrRestoreLeaseExpired = errors.New("database restore lease is no longer active")

type restoreContextKey struct{}

// RestoreLease permits only the owner's explicitly tagged SQL while imports
// drain and replace the published pool. It is not durable allocation authority.
type RestoreLease struct{ active atomic.Bool }

var currentRestoreLease atomic.Pointer[RestoreLease]

func BeginRestore() (*RestoreLease, error) {
	lease := &RestoreLease{}
	lease.active.Store(true)
	if !currentRestoreLease.CompareAndSwap(nil, lease) {
		return nil, ErrRestoreInProgress
	}
	// Admission sees the lease before draining operations. Existing complete
	// transactions retain their read lock until Commit or Rollback.
	if current := GetDB(); current != nil {
		if pool, ok := current.ConnPool.(*admittedPool); ok {
			pool.operations.Lock()
			pool.operations.Unlock()
		}
	}
	return lease, nil
}

func (lease *RestoreLease) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, restoreContextKey{}, lease)
}

func (lease *RestoreLease) Close() {
	if lease == nil || currentRestoreLease.Load() != lease {
		return
	}
	if current := GetDB(); current != nil {
		if pool, ok := current.ConnPool.(*admittedPool); ok {
			pool.operations.Lock()
			defer pool.operations.Unlock()
		}
	}
	if currentRestoreLease.CompareAndSwap(lease, nil) {
		lease.active.Store(false)
	}
}

type admittedPool struct {
	*sql.DB
	expected   *gorm.DB
	operations sync.RWMutex
	closed     bool
}

func (pool *admittedPool) enter(ctx context.Context) error {
	// A callback within a guarded snapshot may make another SQL call. Never
	// wait for an exclusive drain while that caller still owns a read lock.
	if !pool.operations.TryRLock() {
		if currentRestoreLease.Load() != nil {
			return ErrRestoreInProgress
		}
		return ErrDatabaseReplaced
	}
	owner, _ := ctx.Value(restoreContextKey{}).(*RestoreLease)
	active := currentRestoreLease.Load()
	var err error
	switch {
	case owner != nil && (owner != active || !owner.active.Load()):
		err = ErrRestoreLeaseExpired
	case active != nil && owner != active:
		err = ErrRestoreInProgress
	case pool.closed || !databaseReady.Load() || GetDB() != pool.expected:
		err = ErrDatabaseReplaced
	case ctx.Err() != nil:
		err = ctx.Err()
	}
	if err != nil {
		pool.operations.RUnlock()
	}
	return err
}

func (pool *admittedPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := pool.enter(ctx); err != nil {
		return nil, err
	}
	defer pool.operations.RUnlock()
	return pool.DB.ExecContext(ctx, query, args...)
}

func (pool *admittedPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if err := pool.enter(ctx); err != nil {
		return nil, err
	}
	defer pool.operations.RUnlock()
	return pool.DB.QueryContext(ctx, query, args...)
}

func (pool *admittedPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if err := pool.enter(ctx); err != nil {
		// sql.Row has no exported error constructor. This connector returns
		// the admission error without opening a database or executing SQL.
		rejected := sql.OpenDB(rejectedConnector{err})
		row := rejected.QueryRowContext(ctx, "")
		_ = rejected.Close()
		return row
	}
	defer pool.operations.RUnlock()
	return pool.DB.QueryRowContext(ctx, query, args...)
}

func (pool *admittedPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	// Returning a raw prepared statement would let later executions bypass
	// admission. Published application pools do not enable prepared caching.
	return nil, errors.New("prepared statements are unavailable on the lifecycle-protected database pool")
}

func (pool *admittedPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	if err := pool.enter(ctx); err != nil {
		return nil, err
	}
	tx, err := pool.DB.BeginTx(ctx, opts)
	if err != nil {
		pool.operations.RUnlock()
		return nil, err
	}
	protected := &admittedTransaction{Tx: tx, pool: pool, done: make(chan struct{})}
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = protected.Rollback()
			case <-protected.done:
			}
		}()
	}
	return protected, nil
}

func (pool *admittedPool) GetDBConn() (*sql.DB, error) { return pool.DB, nil }

func (pool *admittedPool) close() error {
	pool.operations.Lock()
	defer pool.operations.Unlock()
	pool.closed = true
	return pool.DB.Close()
}

type admittedTransaction struct {
	*sql.Tx
	pool   *admittedPool
	once   sync.Once
	finish sync.Mutex
	done   chan struct{}
}

func (tx *admittedTransaction) Commit() error {
	tx.finish.Lock()
	defer tx.finish.Unlock()
	err := tx.Tx.Commit()
	tx.release()
	return err
}

func (tx *admittedTransaction) Rollback() error {
	tx.finish.Lock()
	defer tx.finish.Unlock()
	err := tx.Tx.Rollback()
	tx.release()
	return err
}

func (tx *admittedTransaction) release() {
	tx.once.Do(func() {
		tx.pool.operations.RUnlock()
		close(tx.done)
	})
}

func (tx *admittedTransaction) GetDBConn() (*sql.DB, error) { return tx.pool.DB, nil }

type rejectedConnector struct{ err error }

func (c rejectedConnector) Connect(context.Context) (driver.Conn, error) { return nil, c.err }
func (c rejectedConnector) Driver() driver.Driver                        { return rejectedDriver{c.err} }

type rejectedDriver struct{ err error }

func (d rejectedDriver) Open(string) (driver.Conn, error) { return nil, d.err }

// WithConnection protects callers that need a raw SQLite connection for a
// deferred read snapshot. A captured old handle cannot bypass the pool gate.
func WithConnection(db *gorm.DB, fn func(*gorm.DB) error) error {
	pool, ok := db.ConnPool.(*admittedPool)
	if !ok {
		return db.Connection(fn)
	}
	if err := pool.enter(db.Statement.Context); err != nil {
		return err
	}
	defer pool.operations.RUnlock()
	return db.Connection(fn)
}
