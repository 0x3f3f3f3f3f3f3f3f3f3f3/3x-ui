package model

import "gorm.io/gorm"

func SaveClientRecord(tx *gorm.DB, record *ClientRecord) error {
	// GORM's Save can allocate a nil embedded pointer while assigning its fields.
	// Omitted policy columns must stay absent rather than become an explicit policy.
	if record.Policy == nil {
		tx = tx.Omit("policy_upload_bytes_per_second", "policy_download_bytes_per_second", "policy_multiplier")
	}
	return tx.Save(record).Error
}
