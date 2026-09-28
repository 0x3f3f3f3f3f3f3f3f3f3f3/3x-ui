package database

import "gorm.io/gorm"

// Check the actual transaction connection; pool connections may have different session settings.
func requireAdmissionDurability(tx *gorm.DB) error {
	switch tx.Name() {
	case "sqlite":
		var synchronous int
		var journal string
		if err := tx.Raw("PRAGMA synchronous").Scan(&synchronous).Error; err != nil {
			return err
		}
		if err := tx.Raw("PRAGMA journal_mode").Scan(&journal).Error; err != nil {
			return err
		}
		if (journal == "wal" && synchronous >= 2) || ((journal == "delete" || journal == "truncate" || journal == "persist") && synchronous >= 3) {
			return nil
		}
		return ErrUsageDurability
	case "postgres":
		var synchronous, fsync string
		if err := tx.Raw("SHOW synchronous_commit").Scan(&synchronous).Error; err != nil {
			return err
		}
		if err := tx.Raw("SHOW fsync").Scan(&fsync).Error; err != nil {
			return err
		}
		if fsync != "on" || (synchronous != "on" && synchronous != "local" && synchronous != "remote_write" && synchronous != "remote_apply") {
			return ErrUsageDurability
		}
	default:
		return ErrUsageDurability
	}
	return nil
}
