package model

type ClientPolicySource struct {
	InstanceID string `gorm:"primaryKey;size:128"`
	NodeKey    string `gorm:"uniqueIndex;not null;size:128"`
	Epoch      int64  `gorm:"not null"`
	Sequence   int64  `gorm:"not null"`
}

type ClientPolicyTotal struct {
	ClientID       string `gorm:"primaryKey;size:36"`
	RawUpload      int64  `gorm:"not null"`
	RawDownload    int64  `gorm:"not null"`
	BilledBytes    int64  `gorm:"not null"`
	UncertainBytes int64  `gorm:"not null"`
}

// Receipts survive client deletion so final usage cannot reach a replacement identity.
type ClientPolicyReceipt struct {
	InstanceID     string `gorm:"primaryKey;size:128"`
	ClientID       string `gorm:"primaryKey;size:36"`
	Epoch          int64  `gorm:"not null"`
	Sequence       int64  `gorm:"not null"`
	PolicyVersion  int64  `gorm:"not null"`
	RawUpload      int64  `gorm:"not null"`
	RawDownload    int64  `gorm:"not null"`
	BilledBytes    int64  `gorm:"not null"`
	Remainder      int64  `gorm:"not null"`
	UncertainBytes int64  `gorm:"not null"`
	ReservedBytes  int64  `gorm:"not null"`
	Revoked        bool   `gorm:"not null"`
	SeedUpload     int64  `gorm:"not null;<-:create"`
	SeedDownload   int64  `gorm:"not null;<-:create"`
	SeedBilled     int64  `gorm:"not null;<-:create"`
}
