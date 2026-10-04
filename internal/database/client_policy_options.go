package database

import "github.com/mhsanaei/3x-ui/v3/internal/database/model"

func migrateClientPolicyOptionsColumns() error {
	m := db.Migrator()
	if !m.HasTable(&model.ClientRecord{}) {
		return nil
	}
	for _, column := range []string{"policy_upload_bytes_per_second", "policy_download_bytes_per_second", "policy_multiplier", "policy_scope", "desired_policy_version", "policy_fingerprint"} {
		if !m.HasColumn(&model.ClientRecord{}, column) {
			if err := m.AddColumn(&model.ClientRecord{}, column); err != nil {
				return err
			}
		}
	}
	return nil
}
