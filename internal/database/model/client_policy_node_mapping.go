package model

// The original coordinator journal owns this evidence. SQL is a disposable
// projection and must never supply enrollment or execution authority.
type ClientPolicyNodeMapping struct {
	AuthorityID    string `gorm:"primaryKey;size:128;uniqueIndex:idx_node_mapping_global,priority:1"`
	Generation     int64  `gorm:"primaryKey;uniqueIndex:idx_node_mapping_global,priority:2"`
	SourceID       string `gorm:"primaryKey;size:128;uniqueIndex:idx_node_mapping_global,priority:3"`
	LocalClientID  string `gorm:"primaryKey;size:36"`
	GlobalClientID string `gorm:"not null;size:36;uniqueIndex:idx_node_mapping_global,priority:4"`
	NodeID         string `gorm:"not null;size:128"`
	MappingJSON    string `gorm:"not null;type:text"`
}
