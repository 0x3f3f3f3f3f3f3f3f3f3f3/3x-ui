package service

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

// ClientPortableTraffic is the client_traffics snapshot carried in export/import.
// model.Client only has the limit (totalGB); usage counters live in this table.
type ClientPortableTraffic struct {
	Up           int64 `json:"up"`
	Down         int64 `json:"down"`
	ResetCount   int   `json:"resetCount"`
	LastOnline   int64 `json:"lastOnline,omitempty"`
	LastSubFetch int64 `json:"lastSubFetch,omitempty"`
}

// ExportAll snapshots clients, attachments, raw usage and optional managed policy.
// Orphans retain an empty attachment array in the portable wire format.
func (s *ClientService) ExportAll() ([]ClientCreatePayload, error) {
	var out []ClientCreatePayload
	read := func(tx *gorm.DB) error {
		var err error
		out, err = s.exportAllTx(tx)
		return err
	}
	db := database.GetDB()
	var err error
	if db.Name() == "sqlite" {
		// SQLite's driver ignores ReadOnly and uses the writer DSN's BEGIN IMMEDIATE.
		// A deferred snapshot on one connection lets WAL accounting keep committing.
		err = db.Connection(func(conn *gorm.DB) (err error) {
			snapshot := conn.Session(&gorm.Session{NewDB: true})
			if err = snapshot.Exec("BEGIN DEFERRED").Error; err != nil {
				return err
			}
			defer func() { err = errors.Join(err, snapshot.Exec("ROLLBACK").Error) }()
			return read(snapshot)
		})
	} else {
		err = db.Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ClientService) exportAllTx(db *gorm.DB) ([]ClientCreatePayload, error) {
	var rows []model.ClientRecord
	if err := db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientCreatePayload, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}

	ids := make([]int, 0, len(rows))
	emails := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].Id)
		if rows[i].Email != "" {
			emails = append(emails, rows[i].Email)
		}
	}

	attachments := make(map[int][]int, len(rows))
	for _, batch := range chunkInts(ids, sqlInChunk) {
		var links []model.ClientInbound
		if err := db.Where("client_id IN ?", batch).Order("inbound_id ASC").Find(&links).Error; err != nil {
			return nil, err
		}
		for _, l := range links {
			attachments[l.ClientId] = append(attachments[l.ClientId], l.InboundId)
		}
	}

	trafficByEmail := make(map[string]*xray.ClientTraffic, len(emails))
	for _, batch := range chunkStrings(emails, sqlInChunk) {
		var traffics []xray.ClientTraffic
		if err := db.Where("email IN ?", batch).Find(&traffics).Error; err != nil {
			return nil, err
		}
		for i := range traffics {
			t := traffics[i]
			trafficByEmail[t.Email] = &t
		}
	}

	for i := range rows {
		client := rows[i].ToClient()
		// The per-inbound flow_override is the reliable flow for multi-inbound
		// clients; the canonical column can be left stale by SyncInbound (#4792).
		if flow, err := s.EffectiveFlow(db, rows[i].Id); err == nil && flow != "" {
			client.Flow = flow
		}
		traffic := trafficByEmail[rows[i].Email]
		policy, err := portablePolicySnapshot(db, rows[i], traffic)
		if err != nil {
			return nil, err
		}
		var portableTraffic *ClientPortableTraffic
		if traffic != nil {
			portableTraffic = &ClientPortableTraffic{Up: traffic.Up, Down: traffic.Down, ResetCount: traffic.ResetCount, LastOnline: traffic.LastOnline, LastSubFetch: traffic.LastSubFetch}
		}
		inboundIDs := attachments[rows[i].Id]
		if inboundIDs == nil {
			inboundIDs = []int{}
		}
		out = append(out, ClientCreatePayload{
			Client:     *client,
			InboundIds: inboundIDs,
			LimitHwid:  rows[i].LimitHwid,
			Traffic:    portableTraffic,
			Policy:     policy,
		})
	}
	return out, nil
}

// ImportClients skips existing identities and commits each complete restoration
// before applying it to Runtime. Attached items retain precedence over orphans.
func (s *ClientService) ImportClients(inboundSvc *InboundService, items []ClientCreatePayload) (BulkCreateResult, bool, error) {
	result := BulkCreateResult{}
	needRestart := false
	for _, attached := range []bool{true, false} {
		for _, item := range items {
			if (len(item.InboundIds) > 0) != attached {
				continue
			}
			committed, restart, err := s.importPortableClient(inboundSvc, item)
			needRestart = needRestart || restart
			if committed {
				result.Created++
				if err != nil {
					return result, needRestart, err
				}
			} else if err != nil {
				email := strings.TrimSpace(item.Client.Email)
				if email == "" {
					email = "(missing email)"
				}
				result.Skipped = append(result.Skipped, BulkCreateReport{Email: email, Reason: err.Error()})
			}
		}
	}
	return result, needRestart, nil
}

// applyPortableTraffic writes the exported counters. Attached clients got their row
// on create; an orphan's row (new, or kept by a keepTraffic delete) is upserted here.
func applyPortableTraffic(tx *gorm.DB, inboundSvc *InboundService, item ClientCreatePayload) error {
	client := item.Client
	client.Email = strings.TrimSpace(client.Email)
	if len(item.InboundIds) == 0 {
		if err := inboundSvc.AddClientStat(tx, 0, &client); err != nil {
			return err
		}
	}
	return tx.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Updates(map[string]any{
		"up":             item.Traffic.Up,
		"down":           item.Traffic.Down,
		"reset_count":    item.Traffic.ResetCount,
		"last_online":    item.Traffic.LastOnline,
		"last_sub_fetch": item.Traffic.LastSubFetch,
	}).Error
}

// DeleteOrphans removes every unattached client plus its traffic, IP log, and
// external links in one transaction; returns how many clients were deleted.
func (s *ClientService) DeleteOrphans() (int, error) {
	db := database.GetDB()
	sub := database.GetDB().Table("client_inbounds").Select("client_id")
	var rows []model.ClientRecord
	if err := db.Where("id NOT IN (?)", sub).Order("id ASC").Find(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	ids := make([]int, 0, len(rows))
	emails := make([]string, 0, len(rows))
	subIDs := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].Id)
		if rows[i].Email != "" {
			emails = append(emails, rows[i].Email)
		}
		subIDs = append(subIDs, rows[i].SubID)
	}
	tombstoneClientEmails(emails)

	if err := runSerializedTx(func(tx *gorm.DB) error {
		if e := adjustGroupBaselinesForRemovedTraffic(tx, emails); e != nil {
			return e
		}
		if e := clearClientHwidsBySubIDTx(tx, subIDs...); e != nil {
			return e
		}
		for _, batch := range chunkInts(ids, sqlInChunk) {
			if e := tx.Where("client_id IN ?", batch).Delete(&model.ClientInbound{}).Error; e != nil {
				return e
			}
			if e := tx.Where("client_id IN ?", batch).Delete(&model.ClientExternalLink{}).Error; e != nil {
				return e
			}
		}
		if len(emails) > 0 {
			for _, batch := range chunkStrings(emails, sqlInChunk) {
				if e := tx.Where("email IN ?", batch).Delete(&xray.ClientTraffic{}).Error; e != nil {
					return e
				}
				if e := tx.Where("client_email IN ?", batch).Delete(&model.InboundClientIps{}).Error; e != nil {
					return e
				}
			}
			if e := clearGlobalTraffic(tx, emails...); e != nil {
				return e
			}
		}
		for _, batch := range chunkInts(ids, sqlInChunk) {
			if e := tx.Where("id IN ?", batch).Delete(&model.ClientRecord{}).Error; e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return len(ids), nil
}
