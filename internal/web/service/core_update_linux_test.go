//go:build linux

package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func copyUpdateTestCore(t *testing.T, source, destination string) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(output, input)
	closeErr := output.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("copy fixture core: %v, %v", err, closeErr)
	}
}

func TestManagedCorePlainNativeStartupFailureRestoresPreviousConfig(t *testing.T) {
	binary := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set the actual managed core for native update recovery")
	}
	setupConflictDB(t)
	bin := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", bin)
	t.Setenv("XUI_LOG_FOLDER", filepath.Join(bin, "logs"))
	copyUpdateTestCore(t, binary, xray.GetBinaryPath())
	target, _ := sshExitTarget(t, "original:")
	changed, _ := sshExitTarget(t, "changed:")
	api, socks := productionSSHAddress(t), productionSSHAddress(t)
	_, apiPort, _ := net.SplitHostPort(api)
	_, socksPort, _ := net.SplitHostPort(socks)
	apiNumber, _ := strconv.Atoi(apiPort)
	socksNumber, _ := strconv.Atoi(socksPort)
	outbound := map[string]any{"protocol": "freedom", "tag": "direct", "settings": map[string]any{"redirect": target}}
	template := map[string]any{
		"log":   map[string]any{"loglevel": "warning"},
		"api":   map[string]any{"tag": "api", "services": []string{"StatsService", "HandlerService", "RoutingService"}},
		"stats": map[string]any{},
		"inbounds": []any{
			map[string]any{"tag": "api", "protocol": "tunnel", "listen": "127.0.0.1", "port": apiNumber, "settings": map[string]any{"address": "127.0.0.1"}},
			map[string]any{"tag": "native", "protocol": "socks", "listen": "127.0.0.1", "port": socksNumber, "settings": map[string]any{"auth": "noauth"}},
		},
		"outbounds": []any{outbound},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}}},
	}
	save := func() {
		t.Helper()
		data, err := json.Marshal(template)
		if err != nil {
			t.Fatal(err)
		}
		if err := (&SettingService{}).saveSetting("xrayTemplateConfig", string(data)); err != nil {
			t.Fatal(err)
		}
	}
	save()
	svc := &XrayService{}
	isManuallyStopped.Store(false)
	t.Cleanup(func() { _ = svc.StopXray(); isManuallyStopped.Store(false); isNeedXrayRestart.Store(false) })
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	if err := waitSSHCoreReady(currentXrayProcess()); err != nil {
		t.Fatal(err)
	}
	sshExitExchange(t, sshExitDial(t, socks, target), "original:")
	before := currentXrayProcess().GetConfig()
	// A new, valid desired config must not replace the working snapshot when
	// recovering from a binary that passed -test but exits during actual startup.
	outbound["settings"] = map[string]any{"redirect": changed}
	save()
	candidate := filepath.Join(t.TempDir(), "failing-core")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"-c\" ]; then exit 29; fi\nexec '%s' \"$@\"\n", strings.ReplaceAll(binary, "'", "'\\''"))
	if err := os.WriteFile(candidate, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	directory, identity := coreUpdateTestBundle(t, candidate)
	replacement, err := updatebundle.PrepareCoreReplacement(t.Context(), directory, identity, xray.GetBinaryPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := replacement.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := svc.activateCoreReplacement(t.Context(), replacement); err == nil {
		t.Fatal("native-only update reported success before runtime readiness")
	}
	if !currentXrayProcess().IsRunning() || !currentXrayProcess().GetConfig().Equals(before) {
		t.Fatal("native recovery applied changed desired config instead of the previous working snapshot")
	}
	sshExitExchange(t, sshExitDial(t, socks, target), "original:")
}

func coreUpdateTestBundle(t *testing.T, core string) (string, updatebundle.ReleaseIdentity) {
	t.Helper()
	info, err := config.GetReleaseInfo()
	if err != nil {
		t.Fatal(err)
	}
	identity := updatebundle.ReleaseIdentity{Repository: updatebundle.ReleaseRepository, Commit: strings.Repeat("a", 40), Tag: "core-update-fixture", Platform: info.Platform}
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	copyUpdateTestCore(t, core, filepath.Join(directory, "bin", xray.GetBinaryName()))
	for _, name := range []string{"x-ui", "update-stage", "update.sh", "install.sh", "x-ui.sh", "x-ui.rc", "x-ui.service.debian", "x-ui.service.arch", "x-ui.service.rhel"} {
		mode := os.FileMode(0o755)
		if strings.HasPrefix(name, "x-ui.service.") {
			mode = 0o644
		}
		if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), mode); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := updatebundle.BuildManifest(t.Context(), directory, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := updatebundle.WriteManifest(directory, manifest); err != nil {
		t.Fatal(err)
	}
	return directory, identity
}

func TestManagedCoreActivationPreservesBillingAndRecoversWorkingProcess(t *testing.T) {
	runManagedCoreActivationRecovery(t, false)
}

func TestManagedCoreActivationPreservesBillingAndRecoversWorkingProcess_Postgres(t *testing.T) {
	runManagedCoreActivationRecovery(t, true)
}

func runManagedCoreActivationRecovery(t *testing.T, postgres bool) {
	for _, name := range []string{"success", "startup-failure", "readiness-timeout", "cancel-during-start", "invalid-desired-config", "missing-api", "canceled", "manually-stopped"} {
		t.Run(name, func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			fixture := newProductionMieruFixture(t, "tcp")
			binary := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
			if err := os.Remove(xray.GetBinaryPath()); err != nil {
				t.Fatal(err)
			}
			copyUpdateTestCore(t, binary, xray.GetBinaryPath())
			beforeFile, err := os.ReadFile(xray.GetBinaryPath())
			if err != nil {
				t.Fatal(err)
			}
			originalHash := sha256.Sum256(beforeFile)
			flows := openProductionMieruFlows(t, fixture.client)
			ledger := database.NewClientUsageLedger(database.GetDB())
			policyID := lookupClientRecord(t, fixture.user.Email).PolicyID
			before, err := ledger.Read(t.Context(), policyID)
			if err != nil || before.Up != 44 || before.Down != 44 || before.Billed != 132 {
				t.Fatalf("fixture was not charged: %+v, %v", before, err)
			}
			candidate := binary
			startedMarker := filepath.Join(t.TempDir(), "candidate-started")
			if name == "startup-failure" || name == "readiness-timeout" || name == "cancel-during-start" {
				candidate = filepath.Join(t.TempDir(), "candidate")
				// Validation/version use a real core; the chosen runtime fails after
				// the old process stops. This is fault injection, not a release proof.
				failure := "exit 29"
				if name != "startup-failure" {
					failure = "printf started > '" + strings.ReplaceAll(startedMarker, "'", "'\\''") + "'; exec /bin/sleep 30"
				}
				script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"-c\" ]; then %s; fi\nexec '%s' \"$@\"\n", failure, strings.ReplaceAll(binary, "'", "'\\''"))
				if err := os.WriteFile(candidate, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			directory, identity := coreUpdateTestBundle(t, candidate)
			replacement, err := updatebundle.PrepareCoreReplacement(t.Context(), directory, identity, xray.GetBinaryPath())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := replacement.Close(); err != nil {
					t.Error(err)
				}
			})
			if name == "invalid-desired-config" {
				if err := (&SettingService{}).saveSetting("xrayTemplateConfig", "{ broken json"); err != nil {
					t.Fatal(err)
				}
			}
			if name == "missing-api" {
				cfg := currentXrayProcess().GetConfig()
				data, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				var desired map[string]any
				if err := json.Unmarshal(data, &desired); err != nil {
					t.Fatal(err)
				}
				delete(desired, "api")
				data, err = json.Marshal(desired)
				if err != nil {
					t.Fatal(err)
				}
				if err := (&SettingService{}).saveSetting("xrayTemplateConfig", string(data)); err != nil {
					t.Fatal(err)
				}
			}
			if name == "manually-stopped" {
				if err := fixture.service.StopXray(); err != nil {
					t.Fatal(err)
				}
			}
			original := currentXrayProcess()
			ctx := t.Context()
			var reconciled chan error
			var reconciledEarly chan bool
			if name == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if name == "cancel-during-start" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				done := make(chan struct{})
				reconciled = make(chan error, 1)
				reconciledEarly = make(chan bool, 1)
				go func() {
					defer close(done)
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						if _, err := os.Stat(startedMarker); err == nil {
							go func() { reconciled <- fixture.service.RestartXray(false) }()
							select {
							case err := <-reconciled:
								reconciledEarly <- true
								reconciled <- err
							case <-time.After(25 * time.Millisecond):
								reconciledEarly <- false
							}
							cancel()
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
						}
					}
				}()
				t.Cleanup(func() { cancel(); <-done })
			}
			err = fixture.service.activateCoreReplacement(ctx, replacement)
			if reconciled != nil {
				select {
				case reconcileErr := <-reconciled:
					if reconcileErr != nil {
						t.Fatalf("queued reconciliation after recovery: %v", reconcileErr)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("queued reconciliation did not finish after update recovery")
				}
				if <-reconciledEarly {
					t.Fatal("ordinary reconciliation entered during core activation/recovery")
				}
			}
			wantError := name != "success" && name != "manually-stopped"
			if (err != nil) != wantError {
				t.Fatalf("activation error = %v, want error %v", err, wantError)
			}
			if name == "cancel-during-start" && !errors.Is(err, context.Canceled) {
				t.Fatalf("startup cancellation was lost: %v", err)
			}
			if name == "manually-stopped" {
				if !isManuallyStopped.Load() || currentXrayProcess().IsRunning() {
					t.Fatal("core update revived an intentionally stopped service")
				}
			} else {
				if !currentXrayProcess().IsRunning() {
					t.Fatalf("lost running core: %v", err)
				}
				if name == "invalid-desired-config" || name == "missing-api" || name == "canceled" {
					if currentXrayProcess() != original {
						t.Fatal("rejected update replaced the original live process")
					}
					for _, flow := range flows {
						flow.echo(t)
					}
				} else {
					if currentXrayProcess() == original {
						t.Fatal("replacement/recovery did not start a new process")
					}
					if name == "startup-failure" && !currentXrayProcess().GetConfig().Equals(original.GetConfig()) {
						t.Fatal("recovery lost the previous working configuration")
					}
					openProductionMieruFlows(t, fixture.client)
				}
			}
			after, readErr := ledger.Read(t.Context(), policyID)
			wantBytes, wantBilled := int64(88), int64(264)
			if name == "manually-stopped" {
				wantBytes, wantBilled = 44, 132
			}
			if readErr != nil || after.Up != wantBytes || after.Down != wantBytes || after.Billed != wantBilled || after.Revision != before.Revision || after.Multiplier != before.Multiplier {
				t.Fatalf("update lost or double-counted billing: before=%+v after=%+v err=%v", before, after, readErr)
			}
			if wantError {
				data, err := os.ReadFile(xray.GetBinaryPath())
				if err != nil || sha256.Sum256(data) != originalHash {
					t.Fatalf("failed update did not restore old executable: %v", err)
				}
			}
		})
	}
}
