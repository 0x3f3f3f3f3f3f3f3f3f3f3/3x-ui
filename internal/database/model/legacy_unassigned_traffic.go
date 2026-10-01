package model

// Raw native counters without a matching traffic row retain their original
// source and label. This table grants no ownership and is not a billing ledger.
type LegacyUnassignedTraffic struct {
	ProcessID string `gorm:"primaryKey;size:36"`
	LabelHash string `gorm:"primaryKey;size:64"`
	// JSON string serialization is reversible for every valid UTF-8 label,
	// including NUL, which PostgreSQL cannot store directly in TEXT.
	Label            string `gorm:"not null;type:text;serializer:json"`
	SourceMode       string `gorm:"not null;size:16"`
	SourceInstanceID string `gorm:"not null;size:36;default:''"`
	RawUpload        int64  `gorm:"not null"`
	RawDownload      int64  `gorm:"not null"`
}
