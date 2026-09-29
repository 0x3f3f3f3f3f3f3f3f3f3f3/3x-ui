package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var (
	ErrCoreUpdateInContainer = errors.New("update the managed fork container image through your container runtime")
	ErrCoreUpdatePlatform    = errors.New("managed core updates currently require a native Linux installation")
	ErrCoreUpdateBusy        = errors.New("a managed core update is already running")
	coreUpdateMu             sync.Mutex
)

// ManagedCoreUpdateTimeout bounds preparation and activation. HTTP callers allow
// additional response time for recovery, which outlives request cancellation.
const ManagedCoreUpdateTimeout = 8 * time.Minute

func managedCoreUpdatePlatform() (string, error) {
	if runtime.GOOS != "linux" {
		return "", ErrCoreUpdatePlatform
	}
	if os.Getenv("XUI_IN_DOCKER") == "true" {
		return "", ErrCoreUpdateInContainer
	}
	for _, path := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(path); err == nil {
			return "", ErrCoreUpdateInContainer
		}
	}
	info, err := config.GetReleaseInfo()
	return info.Platform, err
}

// GetManagedCoreReleases lists package tags and prerelease status. The cache is
// informational: installation resolves and verifies the selected release again.
func (s *ServerService) GetManagedCoreReleases(ctx context.Context) ([]updatebundle.ReleaseCandidate, error) {
	platform, err := managedCoreUpdatePlatform()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.versionsCacheMu.Lock()
	cache := s.versionsCache
	s.versionsCacheMu.Unlock()
	if cache != nil && time.Since(cache.fetchedAt) <= xrayVersionsCacheTTL {
		return slices.Clone(cache.releases), nil
	}
	releases, err := updatebundle.ListReleaseCandidates(ctx, s.settingService.NewProxiedHTTPClient(30*time.Second), platform)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cache != nil {
			logger.Warning("managed core catalog: serving previous list:", err)
			return slices.Clone(cache.releases), nil
		}
		return nil, err
	}
	s.versionsCacheMu.Lock()
	s.versionsCache = &cachedXrayVersions{releases: slices.Clone(releases), fetchedAt: time.Now()}
	s.versionsCacheMu.Unlock()
	return releases, nil
}

func (s *ServerService) UpdateXray(tag string) error {
	return s.UpdateXrayContext(context.Background(), tag)
}

// UpdateXrayContext keeps the current core (including its panel egress proxy)
// alive throughout download, complete bundle verification and runtime preflight.
func (s *ServerService) UpdateXrayContext(ctx context.Context, tag string) error {
	platform, err := managedCoreUpdatePlatform()
	if err != nil {
		return err
	}
	if !coreUpdateMu.TryLock() {
		return ErrCoreUpdateBusy
	}
	defer coreUpdateMu.Unlock()
	if tag == "" {
		return errors.New("select a managed fork release tag")
	}
	ctx, cancel := context.WithTimeout(ctx, ManagedCoreUpdateTimeout)
	defer cancel()
	return s.updateManagedCore(ctx, s.settingService.NewProxiedHTTPClient(5*time.Minute), tag, platform, os.TempDir())
}

func (s *ServerService) updateManagedCore(ctx context.Context, client *http.Client, tag, platform, parent string) error {
	stage, identity, err := updatebundle.DownloadRelease(ctx, client, tag, platform, parent)
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(stage); err != nil {
			logger.Warning("core update stage cleanup:", err)
		}
	}()
	directory := filepath.Join(stage, "x-ui")
	if err := updatebundle.PreflightRelease(ctx, directory, identity, nil); err != nil {
		return err
	}
	replacement, err := updatebundle.PrepareCoreReplacement(ctx, directory, identity, xray.GetBinaryPath())
	if err != nil {
		return err
	}
	defer func() {
		if err := replacement.Close(); err != nil {
			logger.Warning("core update backup cleanup:", err)
		}
	}()
	return s.xrayService.activateCoreReplacement(ctx, replacement)
}

func (s *XrayService) activateCoreReplacement(ctx context.Context, replacement *updatebundle.CoreReplacement) error {
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	original := currentXrayProcess()
	var previousConfig *xray.Config
	if original != nil && original.IsRunning() {
		previousConfig = original.GetConfig()
		if _, err := sshCoreAPIAddress(previousConfig); err != nil {
			return fmt.Errorf("core update requires startup verification of the previous configuration: %w", err)
		}
	}
	if err := replacement.Activate(ctx); err != nil {
		return errors.Join(err, replacement.Restore())
	}
	if isManuallyStopped.Load() && previousConfig == nil {
		replacement.Commit()
		return nil
	}
	updateErr := s.restartXrayLocked(true, ctx)
	if updateErr == nil {
		replacement.Commit()
		return nil
	}
	originalStillRunning := original != nil && currentXrayProcess() == original && original.IsRunning()
	var stopErr error
	if process := currentXrayProcess(); !originalStillRunning && process != nil && process.IsRunning() {
		if err := process.Stop(); err != nil && process.IsRunning() {
			stopErr = fmt.Errorf("candidate could not be stopped for recovery: %w", err)
		}
	}
	if err := replacement.Restore(); err != nil {
		xrayState.holdBack("core update failed; executable recovery failed")
		return errors.Join(updateErr, stopErr, err)
	}
	if stopErr != nil {
		xrayState.holdBack("core update failed; candidate could not be stopped for recovery")
		return errors.Join(updateErr, stopErr)
	}
	if previousConfig == nil || originalStillRunning {
		xrayState.holdBack("core update failed; previous executable retained")
		return updateErr
	}
	restored := xray.NewProcess(previousConfig)
	xrayState.replace(restored)
	s.xrayAPI.StatsLastValues = nil
	err := restored.Start()
	if err == nil {
		// Request cancellation must not interrupt restoring the previous service.
		err = waitCoreReady(context.Background(), restored)
	}
	if err != nil {
		_ = restored.Stop()
		xrayState.holdBack("core update failed; previous process recovery failed")
		return errors.Join(updateErr, fmt.Errorf("previous process recovery failed: %w", err))
	}
	xrayState.holdBack("core update failed; previous executable and configuration restored")
	return fmt.Errorf("previous core restored after update failure: %w", updateErr)
}
