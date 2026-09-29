package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

var ErrClientPolicyLegacyReset = errors.New("client policy was prepared; retry the reset through the managed core")

type ClientTrafficResetRequest struct {
	RequestID string `json:"requestId,omitempty" example:"782f657b-b127-42a9-9511-d7d83d76dc7d"`
	ClientID  string `json:"clientId,omitempty" example:"e18c9a96-71bf-48d4-933f-8b9a46d4290c"`
}

type ClientTrafficBatchResetRequest struct {
	RequestID string `json:"requestId,omitempty" example:"03a0bc3c-8b5f-4573-9ad2-fb6247cfcfc2"`
}

func tryResetManagedClientPolicy(ctx context.Context, email, requestID string) (bool, error) {
	var client model.ClientRecord
	tx := database.GetDB().WithContext(ctx)
	err := tx.Select("stable_id").First(&client, "email = ?", email).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return resetPreparedClientPolicy(ctx, client.StableID, requestID)
}

func resetPreparedClientPolicy(ctx context.Context, clientID, requestID string) (bool, error) {
	prepared, err := requiresManagedClientTrafficReset(database.GetDB().WithContext(ctx), clientID)
	if err != nil || !prepared {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return true, ResetLocalClientPolicy(ctx, clientID, requestID)
}

func requiresManagedClientTrafficReset(tx *gorm.DB, clientID string) (bool, error) {
	var prepared bool
	err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM client_policy_receipts WHERE client_id = ?)
		OR EXISTS(SELECT 1 FROM client_policy_totals WHERE client_id = ?)`, clientID, clientID).Scan(&prepared).Error
	if err != nil || prepared {
		return prepared, err
	}
	process := currentXrayProcess()
	if process == nil {
		return false, nil
	}
	config := process.GetConfig()
	if config == nil || len(config.ClientPolicy) == 0 {
		return false, nil
	}
	var policyConfig conf.ClientPolicyConfig
	if err := json.Unmarshal(config.ClientPolicy, &policyConfig); err != nil {
		return false, err
	}
	for _, policy := range policyConfig.Policies {
		if policy.ClientID == clientID {
			return true, nil
		}
	}
	return false, nil
}

// Bootstrap locks the same client row before capturing its immutable legacy seed.
func guardLegacyClientTrafficReset(tx *gorm.DB, email, expectedClientID string) error {
	var client model.ClientRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("stable_id").First(&client, "email = ?", email).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && expectedClientID == "" {
		return nil
	}
	if err != nil {
		return err
	}
	if expectedClientID != "" && client.StableID != expectedClientID {
		return errors.New("client identity changed; reload before resetting traffic")
	}
	prepared, err := requiresManagedClientTrafficReset(tx, client.StableID)
	if err != nil {
		return err
	}
	if prepared {
		return ErrClientPolicyLegacyReset
	}
	return nil
}
