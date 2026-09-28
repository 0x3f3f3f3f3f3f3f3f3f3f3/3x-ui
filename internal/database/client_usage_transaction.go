package database

import (
	"context"

	"gorm.io/gorm"
)

// SQLite has one writer; queue locally so busy-handler retries cannot starve another client.
// Existing transactions already own the writer and must never reacquire the gate.
func WithClientUsageTx(ctx context.Context, db *gorm.DB, apply func(*gorm.DB) error) error {
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); db.Name() == "sqlite" && !nested {
		value, exists := db.Statement.Settings.Load("xui:client-usage-writer")
		if !exists {
			value, _ = db.Statement.Settings.LoadOrStore("xui:client-usage-writer", make(chan struct{}, 1))
		}
		gate := value.(chan struct{})
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return db.WithContext(ctx).Transaction(apply)
}
