package job

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"
)

type trafficCommitAckPool struct {
	gorm.ConnPool
	loseNext atomic.Bool
}

func (p *trafficCommitAckPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	switch pool := p.ConnPool.(type) {
	case gorm.ConnPoolBeginner:
		tx, err = pool.BeginTx(ctx, options)
	case gorm.TxBeginner:
		tx, err = pool.BeginTx(ctx, options)
	default:
		return nil, gorm.ErrInvalidTransaction
	}
	if err != nil {
		return nil, err
	}
	committer, ok := tx.(gorm.TxCommitter)
	if !ok {
		return nil, gorm.ErrInvalidTransaction
	}
	return &trafficCommitAckTx{ConnPool: tx, transaction: committer, pool: p}, nil
}

type trafficCommitAckTx struct {
	gorm.ConnPool
	transaction gorm.TxCommitter
	pool        *trafficCommitAckPool
}

func (tx *trafficCommitAckTx) Commit() error {
	if err := tx.transaction.Commit(); err != nil {
		return err
	}
	if tx.pool.loseNext.Swap(false) {
		return errors.New("database committed but acknowledgement was lost")
	}
	return nil
}

func (tx *trafficCommitAckTx) Rollback() error { return tx.transaction.Rollback() }

func loseNextTrafficCommitAcknowledgement(t *testing.T, db *gorm.DB) {
	t.Helper()
	configured, statement := db.ConnPool, db.Statement.ConnPool
	pool := &trafficCommitAckPool{ConnPool: statement}
	pool.loseNext.Store(true)
	db.ConnPool, db.Statement.ConnPool = pool, pool
	t.Cleanup(func() { db.ConnPool, db.Statement.ConnPool = configured, statement })
}
