package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestPasswordProxyOwnerFieldsLostAcknowledgement(t *testing.T) {
	for _, testCase := range []string{"mixed/set", "http/set", "mixed/bulk", "http/bulk"} {
		parts := strings.Split(testCase, "/")
		protocol, operation := model.Protocol(parts[0]), parts[1]
		t.Run(testCase, func(t *testing.T) {
			svc, tunnel, owner, _ := setupManagedActivationService(t)
			removeManagedTemplateOptIn(t, svc)
			target, received := passwordRemovalTarget(t, false)
			targetPort := target.Addr().(*net.TCPAddr).Port
			if err := database.GetDB().Model(tunnel).Update("settings", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, targetPort)).Error; err != nil {
				t.Fatal(err)
			}
			probe, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.Addr().(*net.TCPAddr).Port
			if port == tunnel.Port {
				// The saved tunnel is not listening yet. Keep the colliding
				// socket reserved while choosing a distinct password listener.
				replacement, err := net.Listen("tcp4", "127.0.0.1:0")
				_ = probe.Close()
				if err != nil {
					t.Fatal(err)
				}
				probe = replacement
				port = probe.Addr().(*net.TCPAddr).Port
			}
			_ = probe.Close()
			inbounds, clients := &InboundService{}, &ClientService{}
			ib, _, err := inbounds.AddInbound(&model.Inbound{Enable: true, Protocol: protocol, Listen: "127.0.0.1", Port: port, Settings: passwordOwnerSettings(t, protocol,
				map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": owner.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			mode := "http"
			if protocol == model.Mixed {
				mode = "socks"
			}
			flow, err := passwordConfigDial(mode, port, target.Addr().String(), "alice", "resource-secret")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = flow.Close() })
			tunnelFlow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tunnelFlow.Close() })
			managedActivationEcho(t, flow, "hello!")
			managedActivationEcho(t, tunnelFlow, "hello!")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			original, boot := process.GetConfig(), process.TrafficDrainBootID()
			endpoint, err := process.GetAPIEndpoint()
			if err != nil {
				t.Fatal(err)
			}
			upstream := filepath.Join(filepath.Dir(endpoint), "upstream-control.sock")
			if err := os.Rename(endpoint, upstream); err != nil {
				t.Fatal(err)
			}
			var mutations atomic.Int32
			injected := errors.New("injected owner field acknowledgement lost")
			proxy := managedActivationControlProxy(t, upstream, func(_ context.Context, method string) error {
				if strings.HasSuffix(method, "/ApplyPolicies") && mutations.Add(1) == 1 {
					return injected
				}
				return nil
			})
			if err := os.Rename(proxy, endpoint); err != nil {
				t.Fatal(err)
			}
			apply := func(enable bool) error {
				if operation == "bulk" {
					result, restart, err := clients.BulkSetEnable(inbounds, []string{owner.Email, owner.Email}, enable)
					if err != nil {
						return err
					}
					if len(result.Skipped) != 0 {
						return errors.New(result.Skipped[0].Reason)
					}
					if result.Changed != 1 || restart {
						return fmt.Errorf("bulk recovery contract: %+v restart=%v", result, restart)
					}
					return nil
				}
				changed, restart, err := clients.SetClientEnableByEmail(inbounds, owner.Email, enable)
				if err != nil {
					return err
				}
				if restart || changed != enable {
					return fmt.Errorf("set recovery contract changed=%v restart=%v enable=%v", changed, restart, enable)
				}
				return nil
			}
			if err := apply(false); err == nil || !strings.Contains(err.Error(), injected.Error()) || mutations.Load() != 1 {
				t.Fatalf("lost policy acknowledgement was hidden: mutations=%d err=%v", mutations.Load(), err)
			}
			if process.IsRunning() || process.GetConfig() != original {
				t.Fatal("unacknowledged policy update was accepted or left running")
			}
			managedActivationClosed(t, flow)
			managedActivationClosed(t, tunnelFlow)
			var saved model.Inbound
			if err := database.GetDB().First(&saved, ib.Id).Error; err != nil {
				t.Fatal(err)
			}
			_, accounts, err := passwordProxyAccounts(&saved)
			if err != nil || len(accounts) != 1 || len(linksOf(t, ib.Id)) != 1 {
				t.Fatalf("failed runtime apply changed resource accounts: accounts=%+v err=%v", accounts, err)
			}
			if retained, err := clients.GetByID(owner.Id); err != nil || retained.StableID != owner.StableID || retained.Enable {
				t.Fatalf("policy failure lost saved disable: %+v %v", retained, err)
			}
			if err := svc.RestartXray(false); err != nil {
				t.Fatal(err)
			}
			if currentXrayProcess().TrafficDrainBootID() == boot {
				t.Fatal("recovery did not start a new core boot")
			}
			for _, credential := range [][2]string{{"alice", "resource-secret"}, {"", ""}} {
				if stale, err := passwordConfigDial("http", port, target.Addr().String(), credential[0], credential[1]); err == nil {
					_ = stale.Close()
					t.Fatal("recovery restored disabled or anonymous access")
				}
			}
			if err := apply(false); err != nil {
				t.Fatalf("matching field retry failed: %v", err)
			}
			if err := apply(true); err != nil {
				t.Fatal(err)
			}
			freshPassword, err := passwordConfigDial(mode, port, target.Addr().String(), "alice", "resource-secret")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = freshPassword.Close() })
			managedActivationEcho(t, freshPassword, "hello!")
			fresh, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			managedActivationEcho(t, fresh, "hello!")
			for i := 0; i < 2; i++ {
				if _, _, err := svc.GetXrayTraffic(); err != nil {
					t.Fatal(err)
				}
			}
			if total := policyLedgerTotal(t, owner.StableID); received.Load() != 24 || total.RawUpload != 124 || total.RawDownload != 224 || total.BilledBytes != 396 {
				t.Fatalf("policy recovery lost or replayed target usage: target=%d total=%+v", received.Load(), total)
			}
		})
	}
}
