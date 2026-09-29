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

// Membership is captured before execution so retries cannot reset newly created clients.
type ClientTrafficResetBatch struct {
	RequestID      string `gorm:"primaryKey;size:128;<-:create"`
	Scope          string `gorm:"not null;size:128;<-:create"`
	SelectionHash  string `gorm:"not null;size:64;<-:create"`
	TargetsJSON    string `gorm:"not null;type:text;<-:create"`
	ManagedIDsJSON string `gorm:"not null;type:text"`
	Applied        bool   `gorm:"not null"`
	Affected       int    `gorm:"not null"`
	CreatedAt      int64  `gorm:"autoCreateTime:milli;<-:create"`
}

// A reset captures a committed lifetime boundary; request retries never capture it again.
type ClientPolicyReset struct {
	Id             int64  `gorm:"primaryKey;autoIncrement;index:idx_policy_reset_order,priority:2"`
	ClientID       string `gorm:"size:36;not null;uniqueIndex:idx_policy_reset_request,priority:1;index:idx_policy_reset_order,priority:1;<-:create"`
	RequestID      string `gorm:"size:128;not null;uniqueIndex:idx_policy_reset_request,priority:2;<-:create"`
	InstanceID     string `gorm:"size:128;not null;<-:create"`
	Epoch          int64  `gorm:"not null;<-:create"`
	Sequence       int64  `gorm:"not null;<-:create"`
	RawUpload      int64  `gorm:"not null;<-:create"`
	RawDownload    int64  `gorm:"not null;<-:create"`
	BilledBytes    int64  `gorm:"not null;<-:create"`
	Remainder      int64  `gorm:"not null;<-:create"`
	UncertainBytes int64  `gorm:"not null;<-:create"`
	PolicyVersion  int64  `gorm:"not null;<-:create"`
	CreatedAt      int64  `gorm:"autoCreateTime:milli;<-:create"`
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
