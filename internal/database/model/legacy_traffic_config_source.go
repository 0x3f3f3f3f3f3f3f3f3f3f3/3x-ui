package model

// Startup evidence grants no ownership or billing eligibility. Empty digests
// mean unknown; recorded digests are immutable and stability only decreases.
type LegacyTrafficConfigSource struct {
	ProcessID             string `gorm:"primaryKey;size:36"`
	ConfigDigest          string `gorm:"not null;size:64;default:''"`
	EffectiveConfigDigest string `gorm:"not null;size:64;default:''"`
	ConfigStable          bool   `gorm:"not null;default:false"`
}
