package model

import "gorm.io/gorm"

func SaveClientRecord(tx *gorm.DB, record *ClientRecord) error {
	// GORM's Save can allocate a nil embedded pointer while assigning its fields.
	// Omitted policy columns must stay absent rather than become an explicit policy.
	if record.Policy == nil {
		tx = tx.Omit("policy_upload_bytes_per_second", "policy_download_bytes_per_second", "policy_multiplier", "policy_scope")
	}
	return tx.Save(record).Error
}

func (record *ClientRecord) BeforeSave(_ *gorm.DB) error {
	// A nullable field inside an embedded pointer makes GORM allocate the
	// entire policy even for an all-NULL row. Persist scope at the record level.
	record.PolicyScope = nil
	if record.Policy != nil && record.Policy.Scope != nil {
		scope := *record.Policy.Scope
		record.PolicyScope = &scope
	}
	return nil
}

func (record *ClientRecord) AfterFind(_ *gorm.DB) error {
	if record.PolicyScope != nil {
		if record.Policy == nil {
			record.Policy = &ClientPolicyOptions{}
		}
		scope := *record.PolicyScope
		record.Policy.Scope = &scope
	}
	return nil
}
