package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func renewLocalClientPolicies(ctx context.Context, process *xray.Process) error {
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return err
	}
	location, err := (&SettingService{}).GetTimeLocation()
	if err != nil || location == nil {
		location = time.UTC
	}
	now := time.Now().UnixMilli()
	for start := 0; start < len(config.Policies); start += 1000 {
		batch := config.Policies[start:min(start+1000, len(config.Policies))]
		ids := make([]string, len(batch))
		for i, policy := range batch {
			ids[i] = policy.ClientID
		}
		var due []string
		if err := database.GetDB().WithContext(ctx).Model(&model.ClientRecord{}).Where("stable_id IN ? AND expiry_time > 0 AND expiry_time <= ? AND (reset > 0 OR reset_day > 0 OR reset_weekday > 0)", ids, now).Pluck("stable_id", &due).Error; err != nil {
			return err
		}
		if len(due) == 0 {
			continue
		}
		if err := applyLocalClientPolicyReset(ctx, due, func(instanceID string) ([]clientpolicy.Policy, error) {
			if instanceID != config.InstanceID {
				return nil, ErrClientPolicyLedger
			}
			var policies []clientpolicy.Policy
			err := runSerializedTx(func(tx *gorm.DB) error {
				var err error
				policies, err = prepareClientPolicyRenewals(tx.WithContext(ctx), instanceID, due, now, location)
				return err
			})
			return policies, err
		}); err != nil {
			return err
		}
	}
	return nil
}

func prepareClientPolicyRenewals(tx *gorm.DB, instanceID string, ids []string, now int64, location *time.Location) ([]clientpolicy.Policy, error) {
	var clients []model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stable_id IN ?", ids).Order("stable_id").Find(&clients).Error; err != nil {
		return nil, err
	}
	if err := validateLocalClientPolicyResetScope(tx, ids); err != nil {
		return nil, err
	}
	latest, err := latestClientPolicyResets(tx, ids)
	if err != nil {
		return nil, err
	}
	expiries := make(map[string]int64)
	var policies []clientpolicy.Policy
	for _, client := range clients {
		if client.ExpiryTime <= 0 || client.ExpiryTime > now {
			continue
		}
		var traffic xray.ClientTraffic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&traffic, "email = ?", client.Email).Error; err != nil {
			return nil, err
		}
		traffic.ExpiryTime, traffic.Reset, traffic.ResetDay, traffic.ResetWeekday, traffic.ResetMax = client.ExpiryTime, client.Reset, client.ResetDay, client.ResetWeekday, client.ResetMax
		expiry, count := catchUpClientRenewal(&traffic, now, location)
		if count == 0 {
			continue
		}
		if err := tx.Model(&client).Updates(map[string]any{"expiry_time": expiry, "updated_at": now}).Error; err != nil {
			return nil, err
		}
		if err := tx.Model(&traffic).Updates(map[string]any{"expiry_time": expiry, "reset_count": traffic.ResetCount + count}).Error; err != nil {
			return nil, err
		}
		client.ExpiryTime = expiry
		expiries[client.Email] = expiry
		if expiry > now {
			renewed, err := prepareClientPolicyResetsAtTx(tx, instanceID, []string{client.StableID}, uuid.NewString(), now)
			if err != nil {
				return nil, err
			}
			policies = append(policies, renewed...)
		} else {
			policy, err := prepareClientPolicyRecord(tx, client, latest[client.StableID])
			if err != nil {
				return nil, err
			}
			policies = append(policies, policy)
		}
	}
	return policies, updateManagedRenewalInbounds(tx, expiries, now)
}

func updateManagedRenewalInbounds(tx *gorm.DB, expiries map[string]int64, now int64) error {
	emails := make([]string, 0, len(expiries))
	for email := range expiries {
		emails = append(emails, email)
	}
	var ids []int
	if len(emails) == 0 {
		return nil
	}
	if err := tx.Table("client_inbounds ci").Joins("JOIN clients c ON c.id = ci.client_id").Where("c.email IN ?", emails).Distinct().Pluck("ci.inbound_id", &ids).Error; err != nil {
		return err
	}
	for _, batch := range chunkInts(ids, sqlInChunk) {
		var inbounds []model.Inbound
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", batch).Order("id").Find(&inbounds).Error; err != nil {
			return err
		}
		for _, inbound := range inbounds {
			var settings map[string]json.RawMessage
			if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
				return err
			}
			var clients []map[string]json.RawMessage
			if err := json.Unmarshal(settings["clients"], &clients); err != nil {
				return err
			}
			found := false
			for _, client := range clients {
				var email string
				if err := json.Unmarshal(client["email"], &email); err != nil {
					return err
				}
				if expiry, ok := expiries[email]; ok {
					client["expiryTime"], _ = json.Marshal(expiry)
					client["updated_at"], _ = json.Marshal(now)
					found = true
				}
			}
			if !found {
				return errors.New("renewal client missing from inbound settings")
			}
			settings["clients"], _ = json.Marshal(clients)
			raw, err := json.Marshal(settings)
			if err != nil {
				return err
			}
			if err := tx.Model(&inbound).Update("settings", string(raw)).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
