package service

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Inspect the original captured evidence as well as derived seeds: orphan
// traffic and a zero canonical total must never hide consumed or held bytes.
// This is delegation admission only; ordinary local migration keeps history.
func checkNodeDelegationSnapshotFresh(snapshot authorityMigrationSnapshot) error {
	for _, seed := range snapshot.Seeds {
		if seed.Usage != (policyauthority.Usage{}) || seed.WindowUsed != 0 || seed.WindowRemainder != 0 || seed.FrozenBilled != 0 {
			return ErrAuthorityNotInitialized
		}
	}
	for _, record := range snapshot.Records {
		switch record.Kind {
		case "totals":
			var row model.ClientPolicyTotal
			if json.Unmarshal(record.Value, &row) != nil || row.RawUpload != 0 || row.RawDownload != 0 || row.BilledBytes != 0 || row.UncertainBytes != 0 {
				return ErrAuthorityNotInitialized
			}
		case "receipts":
			var row model.ClientPolicyReceipt
			if json.Unmarshal(record.Value, &row) != nil || row.Epoch != 0 || row.Sequence != 0 || row.FirstUsedAt != 0 || row.RawUpload != 0 || row.RawDownload != 0 || row.BilledBytes != 0 || row.Remainder != 0 || row.UncertainBytes != 0 || row.ReservedBytes != 0 || row.SeedUpload != 0 || row.SeedDownload != 0 || row.SeedBilled != 0 {
				return ErrAuthorityNotInitialized
			}
		case "legacy-traffic":
			var row panelxray.ClientTraffic
			if json.Unmarshal(record.Value, &row) != nil || row.Up != 0 || row.Down != 0 {
				return ErrAuthorityNotInitialized
			}
		}
	}
	return nil
}
