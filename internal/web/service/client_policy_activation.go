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
	if process := currentXrayProcess(); process != nil && len(process.GetConfig().ClientPolicy) > 0 {
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
	err = database.GetDB().Model(&model.ClientPolicySource{}).Where("node_key = ? AND epoch > 0", "local").Count(&count).Error
	return count > 0, err
}

func (s *XrayService) restartManagedXrayLocked(isForce bool) error {
	process := currentXrayProcess()
	if process != nil && process.IsRunning() && len(process.GetConfig().ClientPolicy) == 0 {
		return errors.New("live legacy traffic must be drained and settled before managed activation")
	}
	state, err := EnsureLocalClientPolicyState(filepath.Join(config.GetDBFolderPath(), "client-policy"))
	if err != nil {
		return errors.Join(errManagedCandidateUnavailable, err)
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
				applied, applyErr := rt.ApplyManagedConfig(ctx, process, candidate, PrepareLocalClientPolicyBootstrap)
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
	process = xray.NewProcess(candidate)
	xrayState.replace(process)
	s.xrayAPI.StatsLastValues = nil
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return managed.StartManagedProcess(ctx, process, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
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
	diff, ok := xray.ComputeHotDiff(current, &comparable)
	return !ok || len(diff.RemovedUsers) > 0 || len(diff.RemovedInboundTags) > 0
}

func (s *XrayService) ReconcileManagedChange(ctx context.Context) (bool, error) {
	lock.Lock()
	defer lock.Unlock()
	process := currentXrayProcess()
	if process == nil || len(process.GetConfig().ClientPolicy) == 0 {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		s.SetToNeedRestart()
		return true, errors.Join(err, process.Stop())
	}
	if isManuallyStopped.Load() {
		s.SetToNeedRestart()
		return true, nil
	}
	err := s.restartManagedXrayLocked(false)
	if errors.Is(err, errManagedCandidateUnavailable) {
		err = errors.Join(err, process.Stop())
		s.SetToNeedRestart()
	}
	return true, err
}
