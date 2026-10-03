package service

import (
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
)

type authorityResetInboundIdentity struct {
	ID       int
	StableID string
}

type authorityResetInboundStamp struct {
	StableID string
	ResetAt  int64
}

func authorityResetCanonicalInboundID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

func captureAuthorityResetInboundsTx(tx *gorm.DB, scope string) ([]authorityResetInboundIdentity, error) {
	id, err := strconv.Atoi(strings.TrimPrefix(scope, "inbound:"))
	if err != nil || id != -1 && id <= 0 {
		return nil, ErrClientPolicyLedger
	}
	query := tx.Model(&model.Inbound{}).Select("id", "stable_id").Where("id = ?", id)
	if id == -1 {
		query = tx.Model(&model.Inbound{}).Select("id", "stable_id").Where("id > 0")
	}
	var rows []authorityResetInboundIdentity
	if err := query.Order("id").Limit(100001).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > 100000 {
		return nil, ErrClientPolicyLedger
	}
	return rows, nil
}

func validateAuthorityResetInbounds(snapshot authorityResetCaptureSnapshot) error {
	if snapshot.Schema == 1 {
		if len(snapshot.OriginalInbounds) != 0 {
			return ErrClientPolicyLedger
		}
		return nil
	}
	if snapshot.Schema != 2 || !strings.HasPrefix(snapshot.Operation.Scope, "inbound:") || snapshot.Operation.ScheduledAt != 0 || snapshot.Operation.InboundIDsJSON != "[]" || len(snapshot.OriginalInbounds) > 100000 {
		return ErrClientPolicyLedger
	}
	id, err := strconv.Atoi(strings.TrimPrefix(snapshot.Operation.Scope, "inbound:"))
	if err != nil || id != -1 && id <= 0 {
		return ErrClientPolicyLedger
	}
	seen := make(map[string]bool, len(snapshot.OriginalInbounds))
	for i, row := range snapshot.OriginalInbounds {
		if row.ID <= 0 || id != -1 && row.ID != id || !authorityResetCanonicalInboundID(row.StableID) || seen[row.StableID] || i > 0 && row.ID <= snapshot.OriginalInbounds[i-1].ID {
			return ErrClientPolicyLedger
		}
		seen[row.StableID] = true
	}
	return nil
}

func applyAuthorityInboundResetStampsTx(tx *gorm.DB, original []authorityResetInboundIdentity, at int64) ([]authorityResetInboundStamp, error) {
	ids := make([]string, len(original))
	for i, row := range original {
		ids[i] = row.StableID
	}
	var stamps []authorityResetInboundStamp
	for _, batch := range chunkStrings(ids, 1000) {
		var rows []model.Inbound
		if err := tx.Select("stable_id", "last_traffic_reset_time").Where("stable_id IN ?", batch).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			// A later legitimate reset stamp is current even if application of
			// an older captured operation was delayed.
			if row.LastTrafficResetTime < at {
				if err := tx.Model(&model.Inbound{}).Where("stable_id = ? AND last_traffic_reset_time < ?", row.StableID, at).Update("last_traffic_reset_time", at).Error; err != nil {
					return nil, err
				}
				stamps = append(stamps, authorityResetInboundStamp{StableID: row.StableID, ResetAt: at})
			}
		}
	}
	slices.SortFunc(stamps, func(a, b authorityResetInboundStamp) int { return strings.Compare(a.StableID, b.StableID) })
	return stamps, nil
}

func validateAuthorityInboundResetStamps(original authorityResetCaptureSnapshot, prepared authorityResetPreparationSnapshot) error {
	if original.Schema == 1 {
		if len(prepared.InboundStamps) != 0 {
			return ErrClientPolicyLedger
		}
		return nil
	}
	if len(prepared.InboundStamps) > len(original.OriginalInbounds) || len(prepared.InboundStamps) > 0 && len(prepared.ActiveManagedIDs) == 0 {
		return ErrClientPolicyLedger
	}
	members := make(map[string]bool, len(original.OriginalInbounds))
	for _, row := range original.OriginalInbounds {
		members[row.StableID] = true
	}
	for i, row := range prepared.InboundStamps {
		if !members[row.StableID] || row.ResetAt != prepared.ResetAt || i > 0 && row.StableID <= prepared.InboundStamps[i-1].StableID {
			return ErrClientPolicyLedger
		}
	}
	return nil
}
