package service

import (
	"slices"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Prepared identities already own a durable seed; legacy lifecycle writers must
// not change their independent policy restrictions or clear their usage.
func legacyClientTrafficRows(tx *gorm.DB, rows []*xray.ClientTraffic) ([]*xray.ClientTraffic, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	emails := make([]string, len(rows))
	for i, row := range rows {
		emails[i] = row.Email
	}
	byEmail, err := clientRecordsByEmail(tx, emails)
	if err != nil {
		return nil, err
	}
	records := make([]model.ClientRecord, 0, len(byEmail))
	for _, record := range byEmail {
		records = append(records, *record)
	}
	managed, err := managedClientResetIDs(tx, records)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(slices.Clone(rows), func(row *xray.ClientTraffic) bool {
		record := byEmail[row.Email]
		if record == nil {
			return false
		}
		_, found := slices.BinarySearch(managed, record.StableID)
		return found
	}), nil
}
