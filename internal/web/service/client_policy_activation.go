package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var errManagedCandidateUnavailable = errors.New("managed configuration candidate is unavailable")

func (s *InboundService) reconcileManagedChange(inbound *model.Inbound) (bool, error) {
	if inbound.NodeID != nil {
		return false, nil
	}
	rt, err := s.runtimeFor(inbound)
	if err != nil {
		return false, nil
	}
	managed, ok := rt.(panelruntime.ManagedChangeRuntime)
	if !ok {
		return false, nil
	}
	return managed.ReconcileManagedChange(context.Background())
}

func (s *XrayService) managedPolicyRequested() (bool, error) {
	if process := currentXrayProcess(); process != nil && (len(process.GetConfig().ClientPolicy) > 0 || process.FinalTrafficPending()) {
		return true, nil
	}
	raw, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return false, err
	}
	var cfg struct {
		ClientPolicy json.RawMessage `json:"clientPolicy"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return false, err
	}
	if len(cfg.ClientPolicy) > 0 && string(cfg.ClientPolicy) != "null" {
		return true, nil
	}
	var count int64
	db := database.GetDB()
	if err := db.Model(&model.Inbound{}).Where("node_id IS NULL AND enable = ? AND protocol IN ?", true, []model.Protocol{model.Mieru, model.SSH, model.Snell}).Count(&count).Error; err != nil || count > 0 {
		return count > 0, err
	}
	err = db.Model(&model.ClientPolicySource{}).Where("node_key = ? AND (epoch > 0 OR handoff_boot_id <> '')", "local").Count(&count).Error
	if err != nil || count > 0 {
		return count > 0, err
	}
	var clientID int
	err = db.Table("clients c").Select("c.id").
		Joins("JOIN client_inbounds ci ON ci.client_id = c.id").
		Joins("JOIN inbounds i ON i.id = ci.inbound_id").
		Where("i.node_id IS NULL AND i.enable = ?", true).
		Where("i.protocol IN ? OR c.policy_upload_bytes_per_second IS NOT NULL OR c.policy_download_bytes_per_second IS NOT NULL OR c.policy_multiplier IS NOT NULL", []model.Protocol{model.Tunnel, model.Mixed, model.HTTP, model.Mieru, model.SSH, model.Snell}).
		Limit(1).Scan(&clientID).Error
	return clientID != 0, err
}

func (s *XrayService) restartManagedXrayLocked(isForce bool) error {
	process := currentXrayProcess()
	if err := checkLocalLegacyHandoff(process); err != nil {
		return err
	}
	var executable *xray.TrafficHandoffExecutable
	legacy := process != nil && len(process.GetConfig().ClientPolicy) == 0 && (process.IsRunning() || process.FinalTrafficPending())
	if legacy {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := process.CheckTrafficHandoff(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("live legacy traffic cannot be safely drained and settled: %w", err)
		}
		executable, err = process.PinTrafficHandoffExecutable()
		if err != nil {
			return err
		}
		defer executable.Close()
	}
	state, err := EnsureLocalClientPolicyState(filepath.Join(config.GetDBFolderPath(), "client-policy"))
	if err != nil {
		return errors.Join(errManagedCandidateUnavailable, err)
	}
	if legacy {
		compiled, err := s.compileManagedXrayConfig(state)
		if err != nil {
			return errors.Join(errManagedCandidateUnavailable, err)
		}
		if err := compiled.checkLegacyAccounting(process.GetConfig()); err != nil {
			return err
		}
		if conflicts := bindConflicts(compiled.config, process.GetConfig()); len(conflicts) > 0 {
			return errors.Join(errManagedCandidateUnavailable, fmt.Errorf("config refused: %s", conflicts[0]))
		}
		if _, err := localManagedPolicyRuntime(); err != nil {
			return err
		}
		owners := make(map[string]string, len(compiled.records))
		for _, record := range compiled.records {
			owners[record.Email] = record.StableID
		}
		if err := process.PinFinalTrafficOwners(owners); err != nil {
			return err
		}
		boot := process.TrafficDrainBootID()
		if err := beginLegacyHandoff(state.InstanceID, boot); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err = process.SettleFinalTraffic(ctx, func(batch *xray.TrafficBatch) error {
			return s.settleLegacyTrafficBatchChecked(batch, func(tx *gorm.DB, batch *xray.TrafficBatch) error {
				if err := compiled.checkLegacyTrafficOwners(tx, batch); err != nil {
					return err
				}
				return completeLegacyHandoff(tx, state.InstanceID, boot, batch)
			})
		})
		cancel()
		if err != nil {
			return fmt.Errorf("final legacy traffic settlement: %w", err)
		}
		if err := requireLegacyHandoffComplete(state.InstanceID, boot); err != nil {
			return err
		}
		if process.IsRunning() {
			if err := process.Stop(); err != nil {
				return err
			}
		}
	}
	candidate, err := s.managedConfigCandidate(state)
	if err != nil {
		return errors.Join(errManagedCandidateUnavailable, err)
	}
	managed, err := localManagedPolicyRuntime()
	if err != nil {
		return err
	}
	if process != nil && process.IsRunning() {
		if !isForce && process.GetConfig().Equals(candidate) && !isNeedXrayRestart.Load() {
			return nil
		}
		if conflicts := bindConflicts(candidate, process.GetConfig()); len(conflicts) > 0 {
			refused := fmt.Sprintf("config refused: %s", conflicts[0])
			xrayState.holdBack(refused)
			return errors.Join(errManagedCandidateUnavailable, errors.New(refused))
		}
		if !isForce {
			if rt, ok := managed.(panelruntime.ManagedConfigRuntime); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				applied, applyErr := rt.ApplyManagedConfig(ctx, process, candidate, func(caps *command.Capabilities, policy *conf.ClientPolicyConfig) (*panelruntime.ManagedPolicyBootstrap, error) {
					if err := prepareSSHManagedResources(caps, candidate); err != nil {
						return nil, err
					}
					return PrepareLocalClientPolicyBootstrap(caps, policy)
				})
				if applyErr != nil && (errors.Is(applyErr, panelruntime.ErrManagedConfigPartial) || managedAccessChanged(process.GetConfig(), candidate)) {
					applyErr = errors.Join(applyErr, process.Stop())
					s.SetToNeedRestart()
				}
				if applied && applyErr == nil {
					applyErr = pollLocalClientPolicyLedger(ctx, process)
				}
				cancel()
				if applied || applyErr != nil {
					return applyErr
				}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = pollLocalClientPolicyLedger(ctx, process)
		cancel()
		if err != nil {
			if managedAccessChanged(process.GetConfig(), candidate) {
				err = errors.Join(err, process.Stop())
				s.SetToNeedRestart()
			}
			return err
		}
		if err := process.Stop(); err != nil {
			return err
		}
		candidate, err = s.managedConfigCandidate(state)
		if err != nil {
			return err
		}
	}
	var policy conf.ClientPolicyConfig
	if err := json.Unmarshal(candidate.ClientPolicy, &policy); err != nil {
		return err
	}
	if executable != nil {
		process = executable.NewProcess(candidate)
	} else {
		process = xray.NewProcess(candidate)
	}
	xrayState.replace(process)
	s.xrayAPI.StatsLastValues = nil
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return managed.StartManagedProcess(ctx, process, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
		if err := prepareSSHManagedResources(caps, candidate); err != nil {
			return nil, err
		}
		return PrepareLocalClientPolicyBootstrap(caps, &policy)
	})
}

func (s *XrayService) managedConfigCandidate(state *conf.ClientPolicyConfig) (*xray.Config, error) {
	for range 3 {
		candidate, err := s.GetManagedXrayConfig(state)
		if !errors.Is(err, ErrManagedConfigStale) {
			return candidate, err
		}
	}
	return nil, ErrManagedConfigStale
}

func managedAccessChanged(current, next *xray.Config) bool {
	var before, after conf.ClientPolicyConfig
	if json.Unmarshal(current.ClientPolicy, &before) != nil || json.Unmarshal(next.ClientPolicy, &after) != nil {
		return true
	}
	byID := make(map[string]clientpolicy.Policy, len(after.Policies))
	for _, policy := range after.Policies {
		byID[policy.ClientID] = policy
	}
	for _, policy := range before.Policies {
		if updated, exists := byID[policy.ClientID]; !exists || updated != policy {
			return true
		}
	}
	comparable := *next
	comparable.ClientPolicy = current.ClientPolicy
	diff, ok := xray.ComputeManagedHotDiff(current, &comparable)
	return !ok || len(diff.RemovedUsers) > 0 || len(diff.RemovedInboundTags) > 0
}

func (s *XrayService) ReconcileManagedChange(ctx context.Context) (bool, error) {
	lock.Lock()
	defer lock.Unlock()
	process := currentXrayProcess()
	requested, err := s.managedPolicyRequested()
	if err != nil || !requested {
		return requested, err
	}
	wasManaged := process != nil && len(process.GetConfig().ClientPolicy) > 0
	if err := ctx.Err(); err != nil {
		s.SetToNeedRestart()
		if wasManaged {
			err = errors.Join(err, process.Stop())
		}
		return true, err
	}
	if isManuallyStopped.Load() || process == nil {
		s.SetToNeedRestart()
		return true, nil
	}
	err = s.restartManagedXrayLocked(false)
	if wasManaged && errors.Is(err, errManagedCandidateUnavailable) {
		err = errors.Join(err, process.Stop())
	}
	if err != nil {
		s.SetToNeedRestart()
	}
	return true, err
}
