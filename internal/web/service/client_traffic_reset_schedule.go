package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func (s *ClientService) RunScheduledTrafficReset(ctx context.Context, period string, now time.Time) error {
	operation, err := captureScheduledTrafficReset(ctx, period, now)
	if err != nil {
		return err
	}
	_, needRestart, err := s.applyTrafficResetBatch(ctx, &InboundService{}, operation)
	if needRestart {
		(&XrayService{}).SetToNeedRestart()
	}
	return err
}

func trafficResetCalendarWindow(period string, now time.Time) (int64, error) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch period {
	case "hourly":
		return now.Add(-time.Duration(now.Minute())*time.Minute - time.Duration(now.Second())*time.Second - time.Duration(now.Nanosecond())).UnixMilli(), nil
	case "daily", "monthly":
		return day.UnixMilli(), nil
	case "weekly":
		return day.AddDate(0, 0, -int(day.Weekday())).UnixMilli(), nil
	default:
		return 0, errors.New("invalid traffic reset period")
	}
}

func monthlyTrafficResetQuery(tx *gorm.DB, period string, now time.Time) *gorm.DB {
	if period != "monthly" {
		return tx
	}
	last := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()
	switch now.Day() {
	case 1:
		return tx.Where("traffic_reset_day <= ?", 1)
	case last:
		return tx.Where("traffic_reset_day >= ?", last)
	default:
		return tx.Where("traffic_reset_day = ?", now.Day())
	}
}

func captureScheduledTrafficReset(ctx context.Context, period string, now time.Time) (model.ClientTrafficResetBatch, error) {
	var operation model.ClientTrafficResetBatch
	at, err := trafficResetCalendarWindow(period, now)
	if err != nil || at <= 0 {
		return operation, errors.New("invalid traffic reset calendar window")
	}
	zone := sha256.Sum256([]byte(now.Location().String()))
	scope := fmt.Sprintf("calendar:%s:%x", period, zone[:8])
	err = runAuthorityResetCapture(ctx, "", authorityResetCalendarKey(scope, at), &operation, func(tx *gorm.DB) error {
		return selectScheduledTrafficResetTx(tx.WithContext(ctx), &operation, period, now, scope, at)
	}, func(original model.ClientTrafficResetBatch) error {
		if original.Scope != scope || original.ScheduledAt != at {
			return ErrClientPolicyLedger
		}
		return nil
	})
	return operation, err
}

func selectScheduledTrafficResetTx(tx *gorm.DB, operation *model.ClientTrafficResetBatch, period string, now time.Time, scope string, at int64) error {
	err := tx.First(operation, "scope = ? AND scheduled_at = ?", scope, at).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var inbounds []model.Inbound
	if err := monthlyTrafficResetQuery(tx.Where("traffic_reset = ?", period), period, now).Limit(100001).Find(&inbounds).Error; err != nil {
		return err
	}
	if len(inbounds) > 100000 {
		return errors.New("reset selection exceeds 100000 inbounds")
	}
	inboundIDs := make([]int, len(inbounds))
	for i, inbound := range inbounds {
		inboundIDs[i] = inbound.Id
	}
	targets, err := scheduledClientResetTargets(tx, period, now, inboundIDs)
	if err != nil {
		return err
	}
	rawTargets, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	slices.Sort(inboundIDs)
	rawInbounds, err := json.Marshal(inboundIDs)
	if err != nil {
		return err
	}
	*operation = model.ClientTrafficResetBatch{RequestID: uuid.NewString(), Scope: scope, ScheduledAt: at, TargetsJSON: string(rawTargets), InboundIDsJSON: string(rawInbounds), ManagedIDsJSON: "[]"}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(operation).Error; err != nil {
		return err
	}
	*operation = model.ClientTrafficResetBatch{}
	return tx.First(operation, "scope = ? AND scheduled_at = ?", scope, at).Error
}

func scheduledClientResetTargets(tx *gorm.DB, period string, now time.Time, inboundIDs []int) ([]clientResetTarget, error) {
	byID := make(map[string]clientResetTarget)
	for _, batch := range chunkInts(inboundIDs, sqlInChunk) {
		var clients []model.ClientRecord
		if err := tx.Table("clients c").Select("DISTINCT c.*").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").Where("ci.inbound_id IN ?", batch).Limit(100001).Find(&clients).Error; err != nil {
			return nil, err
		}
		for _, client := range clients {
			byID[client.StableID] = clientResetTarget{ClientID: client.StableID, Email: client.Email}
		}
		if len(byID) > 100000 {
			return nil, errors.New("reset selection exceeds 100000 clients")
		}
	}
	var own []model.ClientRecord
	if err := monthlyTrafficResetQuery(tx.Where("traffic_reset = ?", period), period, now).Limit(100001).Find(&own).Error; err != nil {
		return nil, err
	}
	if len(own) > 100000 {
		return nil, errors.New("reset selection exceeds 100000 clients")
	}
	managed, err := managedClientResetIDs(tx, own)
	if err != nil {
		return nil, err
	}
	emails := make([]string, len(own))
	for i, record := range own {
		emails[i] = record.Email
	}
	depleted, err := depletedClientResetEmails(tx, emails)
	if err != nil {
		return nil, err
	}
	for _, client := range own {
		_, prepared := slices.BinarySearch(managed, client.StableID)
		if client.Enable || prepared || depleted[client.Email] {
			byID[client.StableID] = clientResetTarget{ClientID: client.StableID, Email: client.Email, EnableLegacy: true}
		}
	}
	if len(byID) > 100000 {
		return nil, errors.New("reset selection exceeds 100000 clients")
	}
	targets := make([]clientResetTarget, 0, len(byID))
	for _, target := range byID {
		targets = append(targets, target)
	}
	slices.SortFunc(targets, func(a, b clientResetTarget) int { return strings.Compare(a.ClientID, b.ClientID) })
	return targets, nil
}

func depletedClientResetEmails(tx *gorm.DB, emails []string) (map[string]bool, error) {
	depleted := make(map[string]bool)
	for _, batch := range chunkStrings(emails, sqlInChunk) {
		var traffic []xray.ClientTraffic
		if err := tx.Where("email IN ?", batch).Find(&traffic).Error; err != nil {
			return nil, err
		}
		for _, row := range traffic {
			depleted[row.Email] = row.Total > 0 && (row.Up >= row.Total || row.Down >= row.Total-row.Up)
		}
	}
	return depleted, nil
}

func prepareScheduledLegacyReset(tx *gorm.DB, operation model.ClientTrafficResetBatch, legacy []model.ClientRecord) (map[int][]string, []int, error) {
	var targets []clientResetTarget
	if err := json.Unmarshal([]byte(operation.TargetsJSON), &targets); err != nil {
		return nil, nil, err
	}
	wanted := make(map[string]bool)
	for _, target := range targets {
		wanted[target.ClientID] = target.EnableLegacy
	}
	selected := slices.DeleteFunc(slices.Clone(legacy), func(record model.ClientRecord) bool { return !wanted[record.StableID] })
	enabled, err := enableLegacyResetClients(tx, selected)
	if err != nil {
		return nil, nil, err
	}
	var inboundIDs []int
	if err := json.Unmarshal([]byte(operation.InboundIDsJSON), &inboundIDs); err != nil {
		return nil, nil, err
	}
	var applied []int
	for _, batch := range chunkInts(inboundIDs, sqlInChunk) {
		var due []int
		if err := tx.Model(&model.Inbound{}).Where("id IN ? AND last_traffic_reset_time < ?", batch, operation.ScheduledAt).Pluck("id", &due).Error; err != nil {
			return nil, nil, err
		}
		if len(due) == 0 {
			continue
		}
		if err := tx.Model(&model.Inbound{}).Where("id IN ?", due).Updates(map[string]any{"up": 0, "down": 0, "last_traffic_reset_time": operation.ScheduledAt}).Error; err != nil {
			return nil, nil, err
		}
		applied = append(applied, due...)
	}
	return enabled, applied, nil
}

type scheduledRemoteReset struct {
	inbound model.Inbound
	email   string
}

func applyScheduledLegacyReset(ctx context.Context, inboundSvc *InboundService, operation model.ClientTrafficResetBatch, legacy []model.ClientRecord, inboundIDs []int) {
	requests, err := scheduledRemoteResetRequests(database.GetDB(), operation, legacy, inboundIDs)
	if err != nil {
		logger.Warning("Failed to prepare scheduled node traffic reset:", err)
		return
	}
	ids := make([]int, len(requests))
	for i, request := range requests {
		ids[i] = request.inbound.Id
	}
	results, panics := fanoutInboundResults(ids, 8, func(i int) error {
		request := requests[i]
		rt, err := inboundSvc.runtimeFor(&request.inbound)
		if err != nil {
			return err
		}
		pushCtx, cancel := context.WithTimeout(ctx, nodeClientPushTimeout)
		defer cancel()
		if request.email != "" {
			return rt.ResetClientTraffic(pushCtx, &request.inbound, request.email)
		}
		return rt.ResetInboundTraffic(pushCtx, &request.inbound)
	})
	for i, result := range results {
		if err := errors.Join(result, panics[i]); err != nil {
			logger.Warning("Scheduled node traffic reset failed:", err)
		}
	}
}

func scheduledRemoteResetRequests(tx *gorm.DB, operation model.ClientTrafficResetBatch, legacy []model.ClientRecord, inboundIDs []int) ([]scheduledRemoteReset, error) {
	var requests []scheduledRemoteReset
	for _, batch := range chunkInts(inboundIDs, sqlInChunk) {
		var inbounds []model.Inbound
		if err := tx.Where("id IN ? AND node_id IS NOT NULL", batch).Find(&inbounds).Error; err != nil {
			return nil, err
		}
		for _, inbound := range inbounds {
			requests = append(requests, scheduledRemoteReset{inbound: inbound})
		}
	}
	var targets []clientResetTarget
	if err := json.Unmarshal([]byte(operation.TargetsJSON), &targets); err != nil {
		return nil, err
	}
	wanted := make(map[string]bool)
	for _, target := range targets {
		wanted[target.ClientID] = target.EnableLegacy
	}
	var clientIDs []string
	for _, client := range legacy {
		if wanted[client.StableID] {
			clientIDs = append(clientIDs, client.StableID)
		}
	}
	seen := make(map[string]bool)
	for _, batch := range chunkStrings(clientIDs, sqlInChunk) {
		var rows []struct {
			model.Inbound
			ClientEmail string
			ClientID    string
		}
		if err := tx.Table("inbounds i").Select("i.*, c.email AS client_email, c.stable_id AS client_id").
			Joins("JOIN client_inbounds ci ON ci.inbound_id = i.id").Joins("JOIN clients c ON c.id = ci.client_id").
			Where("c.stable_id IN ? AND i.node_id IS NOT NULL", batch).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			key := fmt.Sprintf("%d:%s", *row.NodeID, row.ClientID)
			if !seen[key] {
				seen[key] = true
				requests = append(requests, scheduledRemoteReset{inbound: row.Inbound, email: row.ClientEmail})
			}
		}
	}
	return requests, nil
}

func resumeScheduledTrafficResets(ctx context.Context) error {
	return resumeScheduledTrafficResetsWithApplication(ctx, func(ctx context.Context, operation model.ClientTrafficResetBatch) (int, bool, error) {
		return (&ClientService{}).applyTrafficResetBatch(ctx, &InboundService{}, operation)
	})
}

// Selection and retry ordering are shared with restricted pipeline acceptance;
// the ordinary entry point always supplies source-owned application above.
func resumeScheduledTrafficResetsWithApplication(ctx context.Context, apply func(context.Context, model.ClientTrafficResetBatch) (int, bool, error)) error {
	var pending []model.ClientTrafficResetBatch
	if err := database.GetDB().WithContext(ctx).Where("scheduled_at > 0 AND applied = ?", false).Order("last_attempt_at, scheduled_at DESC, created_at DESC").Limit(8).Find(&pending).Error; err != nil {
		return err
	}
	var failures []error
	for _, operation := range pending {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := runSerializedTx(func(tx *gorm.DB) error {
			return tx.WithContext(ctx).Model(&operation).Where("applied = ?", false).Update("last_attempt_at", time.Now().UnixMilli()).Error
		}); err != nil {
			failures = append(failures, err)
			continue
		}
		_, needRestart, err := apply(ctx, operation)
		if needRestart {
			(&XrayService{}).SetToNeedRestart()
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("calendar request %s: %w", operation.RequestID, err))
		}
	}
	return errors.Join(failures...)
}
