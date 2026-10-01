package model

// NativeSSHHostKey is a business listener's database-owned trust identity.
// Private material belongs in privileged database backups, never public JSON.
type NativeSSHHostKey struct {
	ID            string `json:"id" gorm:"primaryKey;size:36"`
	PrivateKeyPEM string `json:"-" gorm:"not null"`
	PublicKey     string `json:"publicKey" gorm:"not null"`
	Fingerprint   string `json:"fingerprint" gorm:"not null"`
	CreatedAt     int64  `json:"createdAt" gorm:"autoCreateTime:milli"`
}
