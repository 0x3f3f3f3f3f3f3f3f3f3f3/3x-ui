package service

import (
	"encoding/json"
	"errors"
	"net"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

func TestMieruPreviewAndRejectedConfigKeepWorkingFlows(t *testing.T) {
	testMieruPreviewAndRejectedConfigKeepWorkingFlows(t, false)
}

func TestMieruPreviewAndRejectedConfigKeepWorkingFlows_Postgres(t *testing.T) {
	testMieruPreviewAndRejectedConfigKeepWorkingFlows(t, true)
}

func testMieruPreviewAndRejectedConfigKeepWorkingFlows(t *testing.T, postgres bool) {
	t.Helper()
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			var targetUp atomic.Int64
			fixture := newProductionMieruFixtureWithTargets(t, underlay, func(t *testing.T) (net.Addr, net.Addr) {
				return productionMieruCountedEcho(t, "tcp", &targetUp), productionMieruCountedEcho(t, "udp", &targetUp)
			})
			// Consume the creation notification through the same apply path as the panel timer.
			fixture.service.ApplyPendingRestart()
			flows := openProductionMieruFlows(t, fixture.client)
			process, applied := currentXrayProcess(), currentXrayProcess().GetConfig()
			settings := &XraySettingService{}
			original, err := settings.GetXrayConfigTemplate()
			if err != nil {
				t.Fatal(err)
			}
			var template map[string]any
			if err := json.Unmarshal([]byte(original), &template); err != nil {
				t.Fatal(err)
			}
			template["log"] = map[string]any{"loglevel": "error"}
			save := func() {
				t.Helper()
				encoded, err := json.Marshal(template)
				if err != nil {
					t.Fatal(err)
				}
				if err := settings.SaveXraySetting(string(encoded)); err != nil {
					t.Fatal(err)
				}
			}
			save()
			until := time.Now().Add(1100 * time.Millisecond)
			for time.Now().Before(until) {
				if _, err := fixture.service.GetXrayConfig(); err != nil {
					t.Fatal(err)
				}
				for _, flow := range flows {
					flow.echo(t)
				}
				time.Sleep(50 * time.Millisecond)
			}
			routing := template["routing"].(map[string]any)
			routing["rules"] = append(routing["rules"].([]any), map[string]any{
				"type": "field", "ip": []string{"not-a-CIDR"}, "outboundTag": "blocked",
			})
			save()
			var invalidConfig *exec.ExitError
			if err := fixture.service.RestartXray(false); !errors.As(err, &invalidConfig) || invalidConfig.ExitCode() != 23 || !strings.HasPrefix(err.Error(), "xray configuration validation failed:") {
				t.Fatalf("invalid route was not rejected by the core validator: %v", err)
			}
			until = time.Now().Add(1100 * time.Millisecond)
			for time.Now().Before(until) {
				for _, flow := range flows {
					flow.echo(t)
				}
				time.Sleep(50 * time.Millisecond)
			}
			fresh := openProductionMieruFlows(t, productionMieruClient(t, underlay, fixture.address, fixture.user))
			if err := settings.SaveXraySetting(original); err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.RestartXray(false); err != nil {
				t.Fatal(err)
			}
			for _, flow := range append(flows, fresh...) {
				flow.echo(t)
			}
			if current := currentXrayProcess(); current != process || !current.IsRunning() || !current.GetConfig().Equals(applied) {
				t.Fatal("preview or rejected application replaced the running core configuration")
			}
			productionMieruStatus(t, fixture.inbound.Id, "running", 4)
			owner := lookupClientRecord(t, fixture.user.Email)
			account, err := database.NewClientUsageLedger(database.GetDB()).Read(t.Context(), owner.PolicyID)
			observed := targetUp.Load()
			if err != nil || observed < 176 || account.Up != observed || account.Down != observed || account.Billed != 3*observed {
				t.Fatalf("preview/rejection changed single payload billing: received=%d account=%+v err=%v", observed, account, err)
			}
		})
	}
}
