package database

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var (
	ErrUsageConflict   = errors.New("usage report conflicts with its durable counter lifetime")
	ErrUsageClosed     = errors.New("usage counter lifetime is closed")
	ErrUsageBoundary   = errors.New("billing change requires the current revision and final reports from every active meter")
	ErrUsageUntracked  = errors.New("legacy traffic changed outside the client usage ledger")
	ErrUsageQuota      = errors.New("client billed quota is exhausted")
	ErrUsageDisabled   = errors.New("client is disabled")
	ErrUsageExpired    = errors.New("client has expired")
	ErrUsageUnready    = errors.New("client first-use expiry must be activated before admission")
	ErrUsageDurability = errors.New("quota admission requires synchronous, durable database commits")
)

type UsageQuotaError struct{ RawAllowance int64 }

func (*UsageQuotaError) Error() string { return ErrUsageQuota.Error() }
func (*UsageQuotaError) Unwrap() error { return ErrUsageQuota }

type ClientUsageReport struct {
	MeterID  string
	Sequence int64
	Up       int64
	Down     int64
}

type ClientUsageDelta struct {
	Up     int64
	Down   int64
	Billed int64
}

type ClientUsageLedger struct{ db *gorm.DB }

func NewClientUsageLedger(db *gorm.DB) *ClientUsageLedger { return &ClientUsageLedger{db: db} }

func (l *ClientUsageLedger) Read(ctx context.Context, policyID string) (model.ClientUsageAccount, error) {
	var a model.ClientUsageAccount
	err := l.db.WithContext(ctx).Model(&model.ClientUsageAccount{}).
		Joins("JOIN clients ON clients.policy_id = client_usage_accounts.policy_id").
		Where("client_usage_accounts.policy_id = ?", policyID).First(&a).Error
	return a, err
}

func (l *ClientUsageLedger) Ensure(ctx context.Context, policyID string) error {
	return WithClientUsageTx(ctx, l.db, func(tx *gorm.DB) error {
		if err := requireAdmissionDurability(tx); err != nil {
			return err
		}
		_, _, err := lockClientUsage(tx, policyID)
		return err
	})
}

func (l *ClientUsageLedger) Register(ctx context.Context, policyID, source, meterID string) (model.ClientUsageMeter, error) {
	var meter model.ClientUsageMeter
	if _, err := uuid.Parse(meterID); err != nil || source == "" || len(source) > 200 || strings.TrimSpace(source) != source {
		return meter, ErrUsageConflict
	}
	err := WithClientUsageTx(ctx, l.db, func(tx *gorm.DB) error {
		_, a, err := lockClientUsage(tx, policyID)
		if err != nil {
			return err
		}
		err = tx.Where("meter_id = ?", meterID).First(&meter).Error
		if err == nil {
			if meter.PolicyID != policyID || meter.Source != source || meter.Closed || meter.Revision != a.Revision {
				return ErrUsageConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var count int64
		if err := tx.Model(&model.ClientUsageMeter{}).Where("policy_id = ? AND source = ? AND closed = ?", policyID, source, false).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrUsageConflict
		}
		meter = model.ClientUsageMeter{ID: meterID, PolicyID: policyID, Source: source, Revision: a.Revision, Multiplier: a.Multiplier}
		return tx.Create(&meter).Error
	})
	if err != nil {
		return model.ClientUsageMeter{}, err
	}
	return meter, nil
}

func (l *ClientUsageLedger) Apply(ctx context.Context, report ClientUsageReport) (ClientUsageDelta, error) {
	return l.apply(ctx, report, false)
}

// Admit commits quota consumption before forwarding; Apply instead settles already observed usage.
func (l *ClientUsageLedger) Admit(ctx context.Context, report ClientUsageReport) (ClientUsageDelta, error) {
	return l.apply(ctx, report, true)
}

func (l *ClientUsageLedger) apply(ctx context.Context, report ClientUsageReport, admit bool) (ClientUsageDelta, error) {
	var delta ClientUsageDelta
	if err := validateUsageReport(report); err != nil {
		return delta, err
	}
	var hint model.ClientUsageMeter
	if err := l.db.WithContext(ctx).Where("meter_id = ?", report.MeterID).First(&hint).Error; err != nil {
		return delta, err
	}
	err := WithClientUsageTx(ctx, l.db, func(tx *gorm.DB) error {
		if admit {
			if err := requireAdmissionDurability(tx); err != nil {
				return err
			}
		}
		client, a, err := lockClientUsage(tx, hint.PolicyID)
		if err != nil {
			return err
		}
		delta, err = applyClientUsage(tx, client, &a, report, admit)
		return err
	})
	if err != nil {
		return ClientUsageDelta{}, err
	}
	return delta, nil
}

func (l *ClientUsageLedger) ChangeMultiplier(ctx context.Context, policyID string, revision int64, multiplier clientpolicy.Multiplier, finals []ClientUsageReport) (model.ClientUsageAccount, error) {
	return l.transition(ctx, policyID, revision, multiplier, finals, false, false)
}

// Prepaid sources have durable final cursors; observed sources must settle explicitly.
func (l *ClientUsageLedger) ChangeMultiplierAdmitted(ctx context.Context, policyID string, multiplier clientpolicy.Multiplier) (model.ClientUsageAccount, error) {
	return l.transition(ctx, policyID, 0, multiplier, nil, false, true)
}

func (l *ClientUsageLedger) Reset(ctx context.Context, policyID string, revision int64, finals []ClientUsageReport) (model.ClientUsageAccount, error) {
	return l.transition(ctx, policyID, revision, 0, finals, true, false)
}

// ResetAdmitted fences all prepaid sources atomically; observed sources still require final reports.
func (l *ClientUsageLedger) ResetAdmitted(ctx context.Context, policyID string) (model.ClientUsageAccount, error) {
	return l.transition(ctx, policyID, 0, 0, nil, true, true)
}

func (l *ClientUsageLedger) transition(ctx context.Context, policyID string, revision int64, multiplier clientpolicy.Multiplier, finals []ClientUsageReport, reset, admittedOnly bool) (model.ClientUsageAccount, error) {
	var result model.ClientUsageAccount
	if !reset {
		if _, _, err := clientpolicy.Charge(0, multiplier, 0); err != nil {
			return result, err
		}
	}
	err := WithClientUsageTx(ctx, l.db, func(tx *gorm.DB) error {
		if admittedOnly {
			if err := requireAdmissionDurability(tx); err != nil {
				return err
			}
		}
		client, a, err := lockClientUsage(tx, policyID)
		if err != nil {
			return err
		}
		if !admittedOnly && a.Revision != revision {
			return ErrUsageBoundary
		}
		var active []model.ClientUsageMeter
		if err := tx.Where("policy_id = ? AND closed = ?", policyID, false).Order("meter_id").Find(&active).Error; err != nil {
			return err
		}
		if admittedOnly {
			for _, meter := range active {
				if !meter.AdmissionOnly {
					return ErrUsageBoundary
				}
				finals = append(finals, ClientUsageReport{MeterID: meter.ID, Sequence: meter.Sequence, Up: meter.Up, Down: meter.Down})
			}
		}
		if len(active) != len(finals) {
			return ErrUsageBoundary
		}
		byID := make(map[string]ClientUsageReport, len(finals))
		for _, final := range finals {
			if err := validateUsageReport(final); err != nil {
				return err
			}
			if _, exists := byID[final.MeterID]; exists {
				return ErrUsageBoundary
			}
			byID[final.MeterID] = final
		}
		for _, meter := range active {
			final, ok := byID[meter.ID]
			if !ok || final.Sequence < meter.Sequence {
				return ErrUsageBoundary
			}
			if _, err := applyClientUsage(tx, client, &a, final, false); err != nil {
				return err
			}
			if err := tx.Model(&model.ClientUsageMeter{}).Where("meter_id = ?", meter.ID).Update("closed", true).Error; err != nil {
				return err
			}
		}
		if a.Revision == math.MaxInt64 {
			return clientpolicy.ErrOverflow
		}
		a.Revision++
		if reset {
			projected := tx.Model(&xray.ClientTraffic{}).Where("email = ? AND policy_id = ? AND up = ? AND down = ?", client.Email, policyID, a.Up, a.Down).
				Updates(map[string]any{"up": 0, "down": 0})
			if projected.Error != nil {
				return projected.Error
			}
			if projected.RowsAffected != 1 {
				return ErrUsageUntracked
			}
			a.Up, a.Down, a.Billed, a.Remainder = 0, 0, 0, 0
		} else {
			a.Multiplier = int64(multiplier)
		}
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		result = a
		return nil
	})
	if err != nil {
		return model.ClientUsageAccount{}, err
	}
	return result, nil
}

// A first-statement write serializes SQLite and PostgreSQL without read-lock upgrade deadlocks.
func lockClientUsage(tx *gorm.DB, policyID string) (model.ClientRecord, model.ClientUsageAccount, error) {
	var client model.ClientRecord
	var a model.ClientUsageAccount
	locked := tx.Model(&model.ClientRecord{}).Where("policy_id = ?", policyID).UpdateColumn("id", gorm.Expr("id"))
	if locked.Error != nil {
		return client, a, locked.Error
	}
	if locked.RowsAffected != 1 {
		return client, a, gorm.ErrRecordNotFound
	}
	if err := tx.Where("policy_id = ?", policyID).First(&client).Error; err != nil {
		return client, a, err
	}
	err := tx.Where("policy_id = ?", policyID).First(&a).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return client, a, err
	}
	var traffic xray.ClientTraffic
	if err := tx.Where("email = ?", client.Email).First(&traffic).Error; err != nil {
		return client, a, err
	}
	if traffic.PolicyID != "" && traffic.PolicyID != policyID {
		return client, a, ErrUsageConflict
	}
	billed, err := checkedUsageAdd(traffic.Up, traffic.Down)
	if err != nil {
		return client, a, err
	}
	if err := tx.Model(&xray.ClientTraffic{}).Where("id = ?", traffic.Id).Update("policy_id", policyID).Error; err != nil {
		return client, a, err
	}
	a = model.ClientUsageAccount{PolicyID: policyID, Up: traffic.Up, Down: traffic.Down, Billed: billed, Multiplier: 1000, Revision: 1}
	return client, a, tx.Create(&a).Error
}

func applyClientUsage(tx *gorm.DB, client model.ClientRecord, a *model.ClientUsageAccount, r ClientUsageReport, admit bool) (ClientUsageDelta, error) {
	var delta ClientUsageDelta
	var meter model.ClientUsageMeter
	if err := tx.Where("meter_id = ? AND policy_id = ?", r.MeterID, client.PolicyID).First(&meter).Error; err != nil {
		return delta, err
	}
	var quota int64
	if admit {
		var err error
		quota, err = clientAdmissionQuota(tx, client, a)
		if err != nil {
			return delta, err
		}
		if meter.Closed {
			return delta, ErrUsageClosed
		}
		if r.Sequence < meter.Sequence {
			return delta, ErrUsageConflict
		}
	}
	if r.Sequence < meter.Sequence {
		return delta, nil
	}
	if r.Sequence == meter.Sequence {
		if r.Up != meter.Up || r.Down != meter.Down {
			return delta, ErrUsageConflict
		}
		return delta, nil
	}
	if !admit && meter.AdmissionOnly {
		return delta, ErrUsageConflict
	}
	if meter.Closed {
		return delta, ErrUsageClosed
	}
	if meter.Revision != a.Revision || meter.Multiplier != a.Multiplier || r.Up < meter.Up || r.Down < meter.Down {
		return delta, ErrUsageConflict
	}
	delta.Up, delta.Down = r.Up-meter.Up, r.Down-meter.Down
	raw, err := checkedUsageAdd(delta.Up, delta.Down)
	if err != nil {
		return ClientUsageDelta{}, err
	}
	var carry int64
	delta.Billed, carry, err = clientpolicy.Charge(raw, clientpolicy.Multiplier(meter.Multiplier), a.Remainder)
	if err != nil {
		return ClientUsageDelta{}, err
	}
	next := *a
	next.Remainder = carry
	for _, pair := range []struct {
		target *int64
		delta  int64
	}{{&next.Up, delta.Up}, {&next.Down, delta.Down}, {&next.Billed, delta.Billed}} {
		*pair.target, err = checkedUsageAdd(*pair.target, pair.delta)
		if err != nil {
			return ClientUsageDelta{}, err
		}
	}
	if _, err := checkedUsageAdd(next.Up, next.Down); err != nil {
		return ClientUsageDelta{}, err
	}
	if admit && quota > 0 && (next.Billed > quota || (next.Billed == quota && next.Remainder > 0)) {
		allowance, err := clientpolicy.RawAllowance(quota-a.Billed, a.Remainder, clientpolicy.Multiplier(a.Multiplier))
		if err != nil {
			return ClientUsageDelta{}, err
		}
		return ClientUsageDelta{}, &UsageQuotaError{RawAllowance: allowance}
	}
	projected := tx.Model(&xray.ClientTraffic{}).Where("email = ? AND policy_id = ? AND up = ? AND down = ?", client.Email, client.PolicyID, a.Up, a.Down).
		Updates(map[string]any{"up": next.Up, "down": next.Down})
	if projected.Error != nil {
		return ClientUsageDelta{}, projected.Error
	}
	if projected.RowsAffected != 1 {
		return ClientUsageDelta{}, ErrUsageUntracked
	}
	if err := tx.Save(&next).Error; err != nil {
		return ClientUsageDelta{}, err
	}
	meter.Up, meter.Down, meter.Sequence = r.Up, r.Down, r.Sequence
	if err := tx.Save(&meter).Error; err != nil {
		return ClientUsageDelta{}, err
	}
	*a = next
	return delta, nil
}

func clientAdmissionQuota(tx *gorm.DB, client model.ClientRecord, a *model.ClientUsageAccount) (int64, error) {
	var traffic xray.ClientTraffic
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email = ?", client.Email).First(&traffic).Error; err != nil {
		return 0, err
	}
	if traffic.PolicyID != client.PolicyID {
		return 0, ErrUsageConflict
	}
	if !client.Enable || !traffic.Enable {
		return 0, ErrUsageDisabled
	}
	now := time.Now().UnixMilli()
	if (client.ExpiryTime > 0 && client.ExpiryTime <= now) || (traffic.ExpiryTime > 0 && traffic.ExpiryTime <= now) {
		return 0, ErrUsageExpired
	}
	if client.ExpiryTime < 0 || traffic.ExpiryTime < 0 {
		return 0, ErrUsageUnready
	}
	if traffic.Total < 0 || client.TotalGB < 0 {
		return 0, clientpolicy.ErrInvalidUsage
	}
	quota := traffic.Total
	if client.TotalGB > 0 && (quota == 0 || client.TotalGB < quota) {
		quota = client.TotalGB
	}
	if quota > 0 && (a.Billed > quota || (a.Billed == quota && a.Remainder > 0)) {
		return 0, &UsageQuotaError{}
	}
	return quota, nil
}

func validateUsageReport(r ClientUsageReport) error {
	if _, err := uuid.Parse(r.MeterID); err != nil || r.Sequence < 0 || r.Up < 0 || r.Down < 0 {
		return ErrUsageConflict
	}
	return nil
}

func checkedUsageAdd(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, clientpolicy.ErrInvalidUsage
	}
	if b > math.MaxInt64-a {
		return 0, clientpolicy.ErrOverflow
	}
	return a + b, nil
}
