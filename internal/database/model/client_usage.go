package model

type ClientUsageAccount struct {
	PolicyID   string `gorm:"primaryKey;size:36"`
	Up         int64
	Down       int64
	Billed     int64
	Remainder  int64
	Multiplier int64
	Revision   int64
}

// A meter is one authenticated source's cumulative counter lifetime, bounded by policy changes.
type ClientUsageMeter struct {
	ID         string `gorm:"primaryKey;column:meter_id;size:36"`
	PolicyID   string `gorm:"size:36;index;uniqueIndex:idx_client_usage_active_source,where:closed = false"`
	Source     string `gorm:"size:200;uniqueIndex:idx_client_usage_active_source,where:closed = false"`
	Revision   int64
	Multiplier int64
	Sequence   int64
	Up         int64
	Down       int64
	Closed     bool `gorm:"not null;default:false"`
}
