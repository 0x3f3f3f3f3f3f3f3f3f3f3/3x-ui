package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

func TestAuthorityBootstrapInstallsGrantBeforeOpeningBusinessListener(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		fail, recovered bool
	}{{name: "activate"}, {name: "projection-failure", fail: true}, {name: "recovered-old-policy", recovered: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			binary := os.Getenv("XRAY_E2E_BINARY")
			if binary == "" {
				t.Skip("set XRAY_E2E_BINARY to the built grant-capable core")
			}
			j, client := authorityExecutionFixture(t)
			dir, err := os.MkdirTemp("", "authority-bootstrap-")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("retained managed startup fixture: %s", dir)
			t.Setenv("XUI_BIN_FOLDER", dir)
			t.Setenv("XUI_LOG_FOLDER", dir)
			if err := os.Symlink(binary, filepath.Join(dir, panelxray.GetBinaryName())); err != nil {
				t.Fatal(err)
			}
			state := &conf.ClientPolicyConfig{StateFile: filepath.Join(dir, "state.db"), InstanceID: "startup-source", Policies: []clientpolicy.Policy{{ClientID: client, Version: 1, Enabled: true, Multiplier: 1500000, QuotaBytes: 100, UploadRate: 8192, DownloadRate: 8192, BurstBytes: 128}}}
			if err := clientpolicy.CreateStore(state.StateFile, state.InstanceID); err != nil {
				t.Fatal(err)
			}
			if scenario.recovered {
				engine, err := clientpolicy.OpenPersistentEngine(state.StateFile, state.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				if err := engine.Initialize(state.Policies[0], clientpolicy.Usage{}); err != nil {
					_ = engine.Close()
					t.Fatal(err)
				}
				if err := engine.Close(); err != nil {
					t.Fatal(err)
				}
				account, err := j.Account(client)
				if err != nil {
					t.Fatal(err)
				}
				policy := account.Policy
				policy.Version = 2
				if _, err := j.ChangePolicy(policyauthority.ChangeRequest{Identity: j.Identity(), ClientID: client, RequestID: "before-startup-policy-change", ExpectedVersion: 1, Policy: policy}); err != nil {
					t.Fatal(err)
				}
				state.Policies[0].Version = 2
			}
			policyRaw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			echo, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = echo.Close() })
			go func() {
				for {
					conn, err := echo.Accept()
					if err != nil {
						return
					}
					go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
				}
			}()
			probe, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := probe.Addr().String()
			port := probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService"]},"clientPolicy":%s,"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":%q}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, filepath.Join(dir, "control.sock"), policyRaw, port, echo.Addr().(*net.TCPAddr).Port, client)
			var cfg panelxray.Config
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatal(err)
			}
			process := panelxray.NewTestProcess(&cfg, filepath.Join(dir, "bootstrap.json"))
			t.Cleanup(func() { _ = process.Stop() })
			local := panelruntime.NewLocal(panelruntime.LocalDeps{})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			injected := errors.New("startup authority projection failed")
			var execution *authorityExecution
			var grant policyauthority.Grant
			called := false
			err = local.StartManagedProcess(ctx, process, func(ctx context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
				bootstrap, err := PrepareLocalClientPolicyBootstrap(caps, state)
				if err != nil {
					return nil, err
				}
				bootstrap.Authorize = func(ctx context.Context, api *panelxray.ClientPolicyAPI) error {
					called = true
					if conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond); err == nil {
						_ = conn.Close()
						return errors.New("business port opened before durable authority")
					}
					// Preparation may join SQL; the Runtime RPC mutex must be free.
					done := make(chan error, 1)
					go func() { done <- local.ApplyManagedPolicies(ctx, process, state.Policies) }()
					select {
					case err := <-done:
						if err == nil {
							return errors.New("control-only process accepted live policy operation")
						}
					case <-time.After(time.Second):
						return errors.New("Runtime held its mutex across authority preparation")
					}
					execution, err = newAuthorityExecution(ctx, database.GetDB(), j, "local", api)
					if err != nil {
						return err
					}
					if scenario.fail {
						name := "authority_bootstrap_projection_fault"
						db := database.GetDB()
						if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
							if tx.Statement.Table == "client_policy_authority_projections" {
								tx.AddError(injected)
							}
						}); err != nil {
							return err
						}
						t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
					}
					share := policyauthority.Direction{Rate: 4096, Burst: 64}
					grant, err = execution.Authorize(ctx, authorityAllocation{ClientID: client, RequestID: "startup", Capacity: 40, Upload: share, Download: share, LeaseDuration: 3 * time.Second})
					return err
				}
				return bootstrap, nil
			})
			if !called {
				t.Fatalf("managed startup skipped the authority callback: %v", err)
			}
			if scenario.fail {
				if !errors.Is(err, injected) || process.IsRunning() || process.IsControlReady() {
					t.Fatalf("failed authorization left core activated: %v/%v/%v", err, process.IsRunning(), process.IsControlReady())
				}
				if conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond); err == nil {
					_ = conn.Close()
					t.Fatal("failed startup opened business port")
				}
				a, err := j.Account(client)
				if err != nil || a.HeldCapacity != 40 {
					t.Fatalf("failed startup credited issued allowance: %+v/%v", a, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			authorityEndpointExchange(t, address, 5)
			// StartManaged closes its temporary API connection after activation.
			api, err := panelxray.DialClientPolicy(ctx, filepath.Join(dir, "control.sock"), state.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			execution.api = api
			if err := execution.Settle(ctx, grant.GrantID, true, false); err != nil {
				t.Fatal(err)
			}
			a, err := j.Account(client)
			if err != nil || a.Usage != (policyauthority.Usage{RawUpload: 8, RawDownload: 7, BilledBytes: 25, Remainder: 100000}) || a.HeldCapacity != 0 {
				t.Fatalf("activated core settlement: %+v/%v", a, err)
			}
		})
	}
}
