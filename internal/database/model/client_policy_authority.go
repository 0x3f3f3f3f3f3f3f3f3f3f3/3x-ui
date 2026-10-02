package model

// A disposable SQL projection of the independent issuance journal. Identity
// cannot be replaced by an ordinary update or imported accounting snapshot.
type ClientPolicyAuthorityProjection struct {
	ClientID    string `gorm:"primaryKey;size:36;<-:create"`
	AuthorityID string `gorm:"not null;size:128;<-:create"`
	Generation  int64  `gorm:"not null;<-:create"`
	Revision    int64  `gorm:"not null"`
	AccountJSON string `gorm:"not null;type:text"`
}
