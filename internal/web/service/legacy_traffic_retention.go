package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var (
	ErrLegacyTrafficBatch     = errors.New("legacy traffic batch is invalid or differs from its receipt")
	ErrLegacyTrafficRetention = errors.New("retained legacy counter overflows or its source identity changed")
)

type legacyClientCounter struct {
	Label string
	Up    int64
	Down  int64
}

// Clone only immutable counter intent. Display/stat metadata does not affect
// settlement, and a validator must not mutate the caller's pending snapshot.
func normalizeLegacyTrafficBatch(batch *xray.TrafficBatch) (*xray.TrafficBatch, error) {
	if batch == nil || batch.ProcessID == "" || len(batch.ProcessID) > 36 || !utf8.ValidString(batch.ProcessID) || batch.ID == "" || len(batch.ID) > 36 || !utf8.ValidString(batch.ID) || batch.Sequence <= 0 {
		return nil, ErrLegacyTrafficBatch
	}
	copy := *batch
	if copy.SourceMode == "" {
		copy.SourceMode = "unknown"
	}
	switch copy.SourceMode {
	case "unknown", "legacy":
		if copy.SourceInstanceID != "" {
			return nil, ErrLegacyTrafficBatch
		}
	case "managed":
		if copy.SourceInstanceID == "" || len(copy.SourceInstanceID) > 36 || !utf8.ValidString(copy.SourceInstanceID) {
			return nil, ErrLegacyTrafficBatch
		}
	default:
		return nil, ErrLegacyTrafficBatch
	}
	copy.Traffics, copy.ClientTraffics = nil, nil
	seenTraffic, seenClients := make(map[string]bool), make(map[string]bool)
	for _, traffic := range batch.Traffics {
		if traffic == nil {
			continue
		}
		key := "outbound:" + traffic.Tag
		if traffic.IsInbound {
			key = "inbound:" + traffic.Tag
		}
		if traffic.Tag == "" || !utf8.ValidString(traffic.Tag) || traffic.IsInbound == traffic.IsOutbound || traffic.Up < 0 || traffic.Down < 0 || seenTraffic[key] {
			return nil, ErrLegacyTrafficBatch
		}
		seenTraffic[key] = true
		value := *traffic
		copy.Traffics = append(copy.Traffics, &value)
	}
	for _, traffic := range batch.ClientTraffics {
		if traffic == nil {
			continue
		}
		if traffic.Email == "" || !utf8.ValidString(traffic.Email) || traffic.Up < 0 || traffic.Down < 0 || seenClients[traffic.Email] {
			return nil, ErrLegacyTrafficBatch
		}
		seenClients[traffic.Email] = true
		copy.ClientTraffics = append(copy.ClientTraffics, &xray.ClientTraffic{Email: traffic.Email, Up: traffic.Up, Down: traffic.Down})
	}
	sort.Slice(copy.Traffics, func(i, j int) bool {
		a, b := copy.Traffics[i], copy.Traffics[j]
		return a.IsInbound && !b.IsInbound || a.IsInbound == b.IsInbound && a.Tag < b.Tag
	})
	sort.Slice(copy.ClientTraffics, func(i, j int) bool { return copy.ClientTraffics[i].Email < copy.ClientTraffics[j].Email })
	return &copy, nil
}

func legacyTrafficPayloadDigest(batch *xray.TrafficBatch) (string, error) {
	intent := struct {
		ProcessID        string
		Sequence         int64
		ID               string
		Final            bool
		SourceMode       string
		SourceInstanceID string
		Traffics         []*xray.Traffic
		Clients          []legacyClientCounter
	}{
		ProcessID: batch.ProcessID, Sequence: batch.Sequence, ID: batch.ID, Final: batch.Final,
		SourceMode: batch.SourceMode, SourceInstanceID: batch.SourceInstanceID, Traffics: batch.Traffics,
	}
	for _, client := range batch.ClientTraffics {
		intent.Clients = append(intent.Clients, legacyClientCounter{Label: client.Email, Up: client.Up, Down: client.Down})
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func retainUnassignedLegacyTraffic(tx *gorm.DB, batch *xray.TrafficBatch) ([]*xray.ClientTraffic, error) {
	var emails []string
	for _, client := range batch.ClientTraffics {
		if client.Up != 0 || client.Down != 0 {
			// PostgreSQL email TEXT cannot contain NUL. Such an exact native
			// label cannot match a stored row, and must stay unassigned.
			if tx.Name() == "postgres" && strings.ContainsRune(client.Email, 0) {
				continue
			}
			emails = append(emails, client.Email)
		}
	}
	matched := make(map[string]bool)
	for _, part := range chunkStrings(emails, sqlInChunk) {
		var rows []xray.ClientTraffic
		if err := tx.Select("email").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("email IN ?", part).Order("email").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			matched[row.Email] = true
		}
	}
	var known []*xray.ClientTraffic
	var retained []model.LegacyUnassignedTraffic
	for _, client := range batch.ClientTraffics {
		if client.Up == 0 && client.Down == 0 {
			continue
		}
		if matched[client.Email] {
			known = append(known, client)
			continue
		}
		hash := sha256.Sum256([]byte(client.Email))
		retained = append(retained, model.LegacyUnassignedTraffic{
			ProcessID: batch.ProcessID, LabelHash: hex.EncodeToString(hash[:]), Label: client.Email,
			SourceMode: batch.SourceMode, SourceInstanceID: batch.SourceInstanceID,
			RawUpload: client.Up, RawDownload: client.Down,
		})
	}
	sort.Slice(retained, func(i, j int) bool { return retained[i].LabelHash < retained[j].LabelHash })
	conflict := clause.OnConflict{
		Columns: []clause.Column{{Name: "process_id"}, {Name: "label_hash"}},
		DoUpdates: clause.Assignments(map[string]any{
			"raw_upload":   gorm.Expr("legacy_unassigned_traffics.raw_upload + excluded.raw_upload"),
			"raw_download": gorm.Expr("legacy_unassigned_traffics.raw_download + excluded.raw_download"),
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL: "legacy_unassigned_traffics.label = excluded.label AND " +
				"legacy_unassigned_traffics.source_mode = excluded.source_mode AND " +
				"legacy_unassigned_traffics.source_instance_id = excluded.source_instance_id AND " +
				"legacy_unassigned_traffics.raw_upload >= 0 AND legacy_unassigned_traffics.raw_download >= 0 AND " +
				"legacy_unassigned_traffics.raw_upload <= ? - excluded.raw_upload AND " +
				"legacy_unassigned_traffics.raw_download <= ? - excluded.raw_download",
			Vars: []any{int64(math.MaxInt64), int64(math.MaxInt64)},
		}}},
	}
	const batchSize = 128
	for start := 0; start < len(retained); start += batchSize {
		part := retained[start:min(start+batchSize, len(retained))]
		result := tx.Clauses(conflict).Create(&part)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != int64(len(part)) {
			return nil, ErrLegacyTrafficRetention
		}
	}
	return known, nil
}
