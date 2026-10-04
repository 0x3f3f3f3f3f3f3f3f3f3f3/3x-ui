package model

// ClientPolicyCoordinatorSource projects control-owner activation. It is not
// an execution stream and carries no core epoch, sequence, or traffic receipt.
// The independently anchored journal remains the authority for its identity.
type ClientPolicyCoordinatorSource struct {
	NodeKey    string `gorm:"primaryKey;size:128"`
	InstanceID string `gorm:"uniqueIndex;not null;size:128"`
	Activated  bool   `gorm:"not null;default:false"`
}
