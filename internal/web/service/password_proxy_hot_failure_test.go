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

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestPasswordProxyHotPartialFailureStopsAndRecovers(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			svc, tunnel, owner, target := setupManagedActivationService(t)
			probe, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			settings := func(password string) string {
				return passwordOwnerSettings(t, protocol,
					map[string]any{"user": "alice", "pass": password, "ownerClientId": owner.StableID},
					map[string]any{"user": "ALICE", "pass": "alias", "ownerClientId": owner.StableID})
			}
			ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Enable: true, Protocol: protocol, Listen: "127.0.0.1", Port: port, Settings: settings("old")})
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
			old, err := passwordConfigDial(mode, ib.Port, fmt.Sprintf("127.0.0.1:%d", target), "alice", "old")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = old.Close() })
			tunnelFlow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tunnelFlow.Close() })
			managedActivationEcho(t, old, "hello!")
			managedActivationEcho(t, tunnelFlow, "hello!")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			original := process.GetConfig()
			boot := process.TrafficDrainBootID()
			endpoint, err := process.GetAPIEndpoint()
			if err != nil {
				t.Fatal(err)
			}
			upstream := filepath.Join(filepath.Dir(endpoint), "upstream-control.sock")
			if err := os.Rename(endpoint, upstream); err != nil {
				t.Fatal(err)
			}
			var mutations atomic.Int32
			injected := errors.New("injected handler acknowledgement lost")
			proxy := managedActivationControlProxy(t, upstream, func(_ context.Context, method string) error {
				if strings.HasSuffix(method, "/AlterInbound") && mutations.Add(1) == 3 {
					return injected
				}
				return nil
			})
			if err := os.Rename(proxy, endpoint); err != nil {
				t.Fatal(err)
			}
			request := *ib
			request.Settings = settings("new")
			_, _, updateErr := (&InboundService{}).UpdateInbound(&request)
			if updateErr == nil || !strings.Contains(updateErr.Error(), injected.Error()) || mutations.Load() != 3 {
				t.Fatalf("partial handler failure was not reported: mutations=%d err=%v", mutations.Load(), updateErr)
			}
			if process.IsRunning() || process.GetConfig() != original {
				t.Fatal("partially applied candidate was acknowledged or left running")
			}
			managedActivationClosed(t, old)
			managedActivationClosed(t, tunnelFlow)
			if err := svc.RestartXray(false); err != nil {
				t.Fatal(err)
			}
			if currentXrayProcess().TrafficDrainBootID() == boot {
				t.Fatal("failure recovery did not start a fresh core boot")
			}
			fresh, err := passwordConfigDial(mode, ib.Port, fmt.Sprintf("127.0.0.1:%d", target), "alice", "new")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			newTunnel, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = newTunnel.Close() })
			managedActivationEcho(t, fresh, "hello!")
			managedActivationEcho(t, newTunnel, "hello!")
			if stale, err := passwordConfigDial(mode, ib.Port, fmt.Sprintf("127.0.0.1:%d", target), "alice", "old"); err == nil {
				_ = stale.Close()
				t.Fatal("recovery restored the old resource credential")
			}
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 124 || total.RawDownload != 224 || total.BilledBytes != 396 {
				t.Fatalf("partial apply/recovery lost or replayed usage: %+v", total)
			}
		})
	}
}
