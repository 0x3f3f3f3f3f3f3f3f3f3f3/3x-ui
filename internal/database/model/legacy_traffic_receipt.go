package model

// Each child retains its latest committed batch to resolve lost commit acknowledgements.
type LegacyTrafficReceipt struct {
	ProcessID string `gorm:"primaryKey;size:36"`
	Sequence  int64  `gorm:"not null"`
	BatchID   string `gorm:"not null;size:36"`
	// Empty means a historical identity-only receipt, never a reconstructed digest.
	PayloadDigest string `gorm:"not null;size:64;default:''"`
}
