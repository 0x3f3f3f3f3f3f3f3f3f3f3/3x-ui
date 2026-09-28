package service

import (
	"context"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type ClientBilling struct {
	ClientPolicyUsage
	Multiplier string `json:"multiplier" example:"1.5"`
	Exhausted  bool   `json:"exhausted" example:"false"`
}

func clientBillingByID(db *gorm.DB, ids []int) (map[int]*ClientBilling, error) {
	out := make(map[int]*ClientBilling)
	for _, batch := range chunkInts(ids, sqlInChunk) {
		var states []clientPolicyState
		err := db.Table("clients c").Select(`c.id, c.email, c.policy_id, c.total_gb,
			TRUE AS managed, a.up AS raw_up, a.down AS raw_down, a.billed, a.remainder,
			a.multiplier, COALESCE(t.total, 0) AS traffic_quota`).
			Joins("JOIN client_usage_accounts a ON a.policy_id = c.policy_id").
			Joins("LEFT JOIN client_traffics t ON t.email = c.email").
			Where("c.id IN ?", batch).Scan(&states).Error
		if err != nil {
			return nil, err
		}
		for _, state := range states {
			policy, err := state.policy()
			if err != nil {
				return nil, err
			}
			quota := state.TrafficQuota
			if state.TotalGB > 0 && (quota == 0 || state.TotalGB < quota) {
				quota = state.TotalGB
			}
			out[state.Id] = &ClientBilling{
				ClientPolicyUsage: policy.Usage, Multiplier: policy.Multiplier,
				Exhausted: quota > 0 && quota-state.Billed < (state.Multiplier+state.Remainder+999)/1000,
			}
		}
	}
	return out, nil
}

func (s *ClientService) GetBilling(ctx context.Context, clientID int) (*ClientBilling, error) {
	billing, err := clientBillingByID(database.GetDB().WithContext(ctx), []int{clientID})
	return billing[clientID], err
}

func depletedTrafficQuery(tx *gorm.DB, now int64) *gorm.DB {
	q := newClientQuery(tx, now, 0, 0)
	managed := q.from().Select("c.email").Where("a.policy_id IS NOT NULL AND c.reset = 0 AND c.reset_day = 0 AND c.reset_weekday = 0").Where(q.depletedExpr())
	return tx.Model(&xray.ClientTraffic{}).Where("("+legacyUsageOnly+" AND ("+depletedClientsClause+")) OR email IN (?)", now, managed)
}
