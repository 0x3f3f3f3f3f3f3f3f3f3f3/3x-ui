package model

// ClientPolicyCoordinatorNode is a configured inventory connection. Original
// enrollment and resource ownership remain in the coordinator journal.
type ClientPolicyCoordinatorNode struct {
	NodeID      string `gorm:"primaryKey;size:128"`
	SourceID    string `gorm:"uniqueIndex;not null;size:128"`
	InventoryID int    `gorm:"uniqueIndex;not null"`
}
