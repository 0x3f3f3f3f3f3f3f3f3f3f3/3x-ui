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

func TestPasswordProxyOwnerRemovalLostAcknowledgement(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
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
			injected := errors.New("injected owner removal acknowledgement lost")
			proxy := managedActivationControlProxy(t, upstream, func(_ context.Context, method string) error {
				if strings.HasSuffix(method, "/AlterInbound") && mutations.Add(1) == 1 {
					return injected
				}
				return nil
			})
			if err := os.Rename(proxy, endpoint); err != nil {
				t.Fatal(err)
			}
			if _, err := clients.Detach(inbounds, owner.Id, []int{ib.Id}); err == nil || !strings.Contains(err.Error(), injected.Error()) || mutations.Load() != 1 {
				t.Fatalf("lost removal acknowledgement was hidden: mutations=%d err=%v", mutations.Load(), err)
			}
			if process.IsRunning() || process.GetConfig() != original {
				t.Fatal("unacknowledged removal was accepted or left running")
			}
			managedActivationClosed(t, flow)
			managedActivationClosed(t, tunnelFlow)
			var saved model.Inbound
			if err := database.GetDB().First(&saved, ib.Id).Error; err != nil {
				t.Fatal(err)
			}
			_, accounts, err := passwordProxyAccounts(&saved)
			if err != nil || len(accounts) != 0 || len(linksOf(t, ib.Id)) != 0 {
				t.Fatalf("failed runtime apply lost the saved removal: accounts=%+v err=%v", accounts, err)
			}
			if retained, err := clients.GetByID(owner.Id); err != nil || retained.StableID != owner.StableID {
				t.Fatalf("detach failure lost reusable identity: %+v %v", retained, err)
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
					t.Fatal("recovery restored removed or anonymous access")
				}
			}
			if _, err := clients.Detach(inbounds, owner.Id, []int{ib.Id}); err != nil {
				t.Fatalf("removal retry failed: %v", err)
			}
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
			if total := policyLedgerTotal(t, owner.StableID); received.Load() != 18 || total.RawUpload != 118 || total.RawDownload != 218 || total.BilledBytes != 372 {
				t.Fatalf("removal recovery lost or replayed target usage: target=%d total=%+v", received.Load(), total)
			}
		})
	}
}
