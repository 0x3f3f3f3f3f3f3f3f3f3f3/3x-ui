package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

var (
	ErrClientPolicyConflict    = errors.New("client policy changed; reload before saving")
	ErrClientPolicyUnsupported = errors.New("client policy execution is not supported by every attachment")
)

type ClientPolicyUpdate struct {
	PolicyID    string `json:"policyId" example:"b2345678-1234-4234-8234-123456789012"`
	Version     int64  `json:"version" example:"0"`
	UploadBps   int64  `json:"uploadBps" example:"65536"`
	DownloadBps int64  `json:"downloadBps" example:"131072"`
	Multiplier  string `json:"multiplier" example:"1.5"`
	Scope       string `json:"scope" example:"local"`
}

type ClientPolicyUsage struct {
	Up        string `json:"up" example:"1024"`
	Down      string `json:"down" example:"2048"`
	Billed    string `json:"billed" example:"4608"`
	Quota     string `json:"quota" example:"1073741824"`
	Remaining string `json:"remaining" example:"1073737216"`
	Unlimited bool   `json:"unlimited" example:"false"`
	Remainder int64  `json:"remainder" example:"0"`
}

type ClientPolicy struct {
	ClientPolicyUpdate
	Supported bool              `json:"supported" example:"true"`
	Usage     ClientPolicyUsage `json:"usage"`
}

type clientPolicyState struct {
	model.ClientRecord `gorm:"embedded"`
	PolicyVersion      int64
	UploadBps          int64
	DownloadBps        int64
	Scope              string
	Managed            bool
	RawUp              int64
	RawDown            int64
	Billed             int64
	Remainder          int64
	Multiplier         int64
	TrafficQuota       int64
	Supported          bool
}

// One statement keeps rates, multiplier and counters in the same database snapshot.
func readClientPolicy(tx *gorm.DB, email string) (clientPolicyState, error) {
	var state clientPolicyState
	err := tx.Table("clients c").Select(`c.*, COALESCE(p.version, 0) AS policy_version,
		COALESCE(p.upload_bps, 0) AS upload_bps, COALESCE(p.download_bps, 0) AS download_bps,
		COALESCE(p.scope, 'local') AS scope, (a.policy_id IS NOT NULL) AS managed,
		COALESCE(a.up, t.up, 0) AS raw_up, COALESCE(a.down, t.down, 0) AS raw_down,
		COALESCE(a.billed, 0) AS billed, COALESCE(a.remainder, 0) AS remainder,
		COALESCE(a.multiplier, 1000) AS multiplier, COALESCE(t.total, 0) AS traffic_quota,
		(a.policy_id IS NOT NULL AND EXISTS (SELECT 1 FROM client_inbounds ci WHERE ci.client_id = c.id)
		AND NOT EXISTS (SELECT 1 FROM client_inbounds ci JOIN inbounds i ON i.id = ci.inbound_id
		WHERE ci.client_id = c.id AND (i.protocol NOT IN ? OR i.node_id IS NOT NULL))) AS supported`, []model.Protocol{model.SSH, model.Mieru}).
		Joins("LEFT JOIN client_policy_settings p ON p.policy_id = c.policy_id").
		Joins("LEFT JOIN client_usage_accounts a ON a.policy_id = c.policy_id").
		Joins("LEFT JOIN client_traffics t ON t.email = c.email").Where("c.email = ?", email).Take(&state).Error
	return state, err
}

func (state clientPolicyState) policy() (ClientPolicy, error) {
	if state.RawUp < 0 || state.RawDown < 0 || state.TotalGB < 0 || state.TrafficQuota < 0 || state.Billed < 0 {
		return ClientPolicy{}, clientpolicy.ErrInvalidUsage
	}
	if !state.Managed {
		if state.RawDown > math.MaxInt64-state.RawUp {
			return ClientPolicy{}, clientpolicy.ErrOverflow
		}
		state.Billed = state.RawUp + state.RawDown
	}
	if _, _, err := clientpolicy.Charge(0, clientpolicy.Multiplier(state.Multiplier), state.Remainder); err != nil {
		return ClientPolicy{}, err
	}
	quota := state.TrafficQuota
	if state.TotalGB > 0 && (quota == 0 || state.TotalGB < quota) {
		quota = state.TotalGB
	}
	return ClientPolicy{
		ClientPolicyUpdate: ClientPolicyUpdate{PolicyID: state.PolicyID, Version: state.PolicyVersion, UploadBps: state.UploadBps, DownloadBps: state.DownloadBps, Multiplier: clientpolicy.Multiplier(state.Multiplier).String(), Scope: state.Scope},
		Supported:          state.Supported,
		Usage: ClientPolicyUsage{
			Up: strconv.FormatInt(state.RawUp, 10), Down: strconv.FormatInt(state.RawDown, 10), Billed: strconv.FormatInt(state.Billed, 10),
			Quota: strconv.FormatInt(quota, 10), Remaining: strconv.FormatInt(max(0, quota-state.Billed), 10), Unlimited: quota == 0, Remainder: state.Remainder,
		},
	}, nil
}

func (s *ClientService) GetPolicy(ctx context.Context, email string) (ClientPolicy, error) {
	state, err := readClientPolicy(database.GetDB().WithContext(ctx), email)
	if err != nil {
		return ClientPolicy{}, err
	}
	return state.policy()
}

func (s *ClientService) UpdatePolicy(ctx context.Context, email string, request ClientPolicyUpdate) (ClientPolicy, error) {
	var result ClientPolicy
	if request.UploadBps < 0 || request.UploadBps > clientpolicy.MaxRate || request.DownloadBps < 0 || request.DownloadBps > clientpolicy.MaxRate {
		return result, clientpolicy.ErrInvalidRate
	}
	multiplier, err := clientpolicy.ParseMultiplier(request.Multiplier)
	if err != nil {
		return result, err
	}
	if request.Scope != "local" {
		return result, ErrClientPolicyUnsupported
	}
	var client model.ClientRecord
	if err := database.GetDB().WithContext(ctx).Where("email = ?", email).First(&client).Error; err != nil {
		return result, err
	}
	var inbounds []model.Inbound
	err = runSerializedTxContext(ctx, func(tx *gorm.DB) error {
		if err := lockUsageClientTx(tx, client.Id); err != nil {
			return err
		}
		state, err := readClientPolicy(tx, email)
		if err != nil {
			return err
		}
		if state.PolicyID != client.PolicyID || request.PolicyID != state.PolicyID || request.Version != state.PolicyVersion {
			return ErrClientPolicyConflict
		}
		if !state.Supported {
			return ErrClientPolicyUnsupported
		}
		if state.PolicyVersion == math.MaxInt64 {
			return clientpolicy.ErrOverflow
		}
		if state.Multiplier != int64(multiplier) {
			if _, err := database.NewClientUsageLedger(tx).ChangeMultiplierAdmitted(ctx, client.PolicyID, multiplier); err != nil {
				return err
			}
		}
		settings := model.ClientPolicySettings{PolicyID: client.PolicyID, UploadBps: request.UploadBps, DownloadBps: request.DownloadBps, Scope: request.Scope, Version: state.PolicyVersion + 1}
		if err := tx.Save(&settings).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Inbound{}).Joins("JOIN client_inbounds ci ON ci.inbound_id = inbounds.id").Where("ci.client_id = ?", client.Id).Find(&inbounds).Error; err != nil {
			return err
		}
		state, err = readClientPolicy(tx, email)
		if err != nil {
			return err
		}
		client = state.ClientRecord
		result, err = state.policy()
		return err
	})
	if err != nil {
		return ClientPolicy{}, err
	}
	for _, inbound := range inbounds {
		rt, push, _, err := (&InboundService{}).nodePushPlan(&inbound)
		if err == nil && push {
			err = rt.UpdateUser(ctx, &inbound, client.Email, *client.ToClient())
		}
		if err != nil {
			return result, fmt.Errorf("policy saved; runtime update failed: %w", err)
		}
	}
	return result, nil
}
