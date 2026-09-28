package service

import (
	"errors"
	"strconv"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var errPortablePolicyImportOnly = errors.New("portable policy snapshots require the import endpoint")

type ClientPortablePolicy struct {
	FormatVersion int    `json:"formatVersion" example:"1"`
	UploadBps     int64  `json:"uploadBps" example:"65536"`
	DownloadBps   int64  `json:"downloadBps" example:"131072"`
	Scope         string `json:"scope" example:"local"`
	Multiplier    string `json:"multiplier" example:"2"`
	Billed        string `json:"billed" example:"5"`
	Remainder     int64  `json:"remainder" example:"500"`
	TrafficTotal  string `json:"trafficTotal" example:"1000"`
	TrafficEnable bool   `json:"trafficEnable" example:"true"`
	TrafficExpiry int64  `json:"trafficExpiry" example:"0"`
}

type portablePolicyValues struct {
	billed, total int64
	multiplier    clientpolicy.Multiplier
}

func parsePortablePolicy(policy *ClientPortablePolicy) (portablePolicyValues, error) {
	var values portablePolicyValues
	if policy.FormatVersion != 1 {
		return values, errors.New("unsupported portable policy formatVersion")
	}
	if policy.Scope != "local" {
		return values, ErrClientPolicyUnsupported
	}
	if policy.UploadBps < 0 || policy.UploadBps > clientpolicy.MaxRate || policy.DownloadBps < 0 || policy.DownloadBps > clientpolicy.MaxRate {
		return values, clientpolicy.ErrInvalidRate
	}
	var err error
	values.multiplier, err = clientpolicy.ParseMultiplier(policy.Multiplier)
	if err != nil {
		return values, err
	}
	if policy.Remainder < 0 || policy.Remainder >= 1000 {
		return values, clientpolicy.ErrInvalidUsage
	}
	values.billed, err = strconv.ParseInt(policy.Billed, 10, 64)
	if err != nil || values.billed < 0 || strconv.FormatInt(values.billed, 10) != policy.Billed {
		return values, clientpolicy.ErrInvalidUsage
	}
	values.total, err = strconv.ParseInt(policy.TrafficTotal, 10, 64)
	if err != nil || values.total < 0 || strconv.FormatInt(values.total, 10) != policy.TrafficTotal {
		return values, clientpolicy.ErrInvalidUsage
	}
	return values, nil
}

func portablePolicySnapshot(tx *gorm.DB, record model.ClientRecord, traffic *xray.ClientTraffic) (*ClientPortablePolicy, error) {
	state, err := readClientPolicy(tx, record.Email)
	if err != nil {
		return nil, err
	}
	if !state.Managed {
		if state.PolicyVersion != 0 || state.UploadBps != 0 || state.DownloadBps != 0 || state.Scope != "local" {
			return nil, database.ErrUsageUnready
		}
		return nil, nil
	}
	if traffic == nil || traffic.PolicyID != record.PolicyID || traffic.Up != state.RawUp || traffic.Down != state.RawDown {
		return nil, database.ErrUsageUnready
	}
	policy := &ClientPortablePolicy{
		FormatVersion: 1, UploadBps: state.UploadBps, DownloadBps: state.DownloadBps,
		Scope: state.Scope, Multiplier: clientpolicy.Multiplier(state.Multiplier).String(),
		Billed: strconv.FormatInt(state.Billed, 10), Remainder: state.Remainder,
		TrafficTotal: strconv.FormatInt(traffic.Total, 10), TrafficEnable: traffic.Enable, TrafficExpiry: traffic.ExpiryTime,
	}
	if _, err := parsePortablePolicy(policy); err != nil {
		return nil, err
	}
	return policy, nil
}

func restorePortablePolicyTx(tx *gorm.DB, record model.ClientRecord, policy *ClientPortablePolicy) error {
	values, err := parsePortablePolicy(policy)
	if err != nil {
		return err
	}
	settings := model.ClientPolicySettings{PolicyID: record.PolicyID, Version: 1, UploadBps: policy.UploadBps, DownloadBps: policy.DownloadBps, Scope: policy.Scope}
	if err := tx.Create(&settings).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.ClientUsageAccount{}).Where("policy_id = ?", record.PolicyID).
		Updates(map[string]any{"billed": values.billed, "remainder": policy.Remainder, "multiplier": int64(values.multiplier)}).Error; err != nil {
		return err
	}
	return tx.Model(&xray.ClientTraffic{}).Where("email = ? AND policy_id = ?", record.Email, record.PolicyID).
		Updates(map[string]any{"total": values.total, "enable": policy.TrafficEnable, "expiry_time": policy.TrafficExpiry}).Error
}
