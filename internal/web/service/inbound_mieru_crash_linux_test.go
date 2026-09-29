package service

import (
	"errors"
	"net"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestMieruInboundKilledCoreProtectsAndRecovers(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			fixture := newProductionMieruFixture(t, underlay)
			live := openProductionMieruFlows(t, fixture.client)
			record := lookupClientRecord(t, fixture.user.Email)
			ledger := database.NewClientUsageLedger(database.GetDB())
			before, err := ledger.Read(t.Context(), record.PolicyID)
			if err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			killedAt := time.Now()
			killSSHExitCore(t, xray.GetBinaryPath(), xray.GetConfigPath())
			requireProductionMieruClosed(t, live)
			if time.Since(killedAt) > 1250*time.Millisecond {
				t.Fatalf("SIGKILL cutoff exceeded 1250ms: %s", time.Since(killedAt))
			}
			deadline := killedAt.Add(2 * time.Second)
			for process.IsRunning() && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			var exitError *exec.ExitError
			if process.IsRunning() || !errors.As(process.GetErr(), &exitError) {
				t.Fatalf("owned core did not record abnormal exit: running=%t error=%v", process.IsRunning(), process.GetErr())
			}
			status, ok := exitError.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("owned core did not exit from SIGKILL: %v", exitError)
			}
			requireProductionMieruDenied(t, fixture.client)
			if underlay == "tcp" {
				probe, err := net.Listen("tcp", fixture.address.String())
				if err != nil {
					t.Fatalf("crashed core left public TCP listener open: %v", err)
				}
				_ = probe.Close()
			} else {
				probe, err := net.ListenPacket("udp", fixture.address.String())
				if err != nil {
					t.Fatalf("crashed core left public UDP listener open: %v", err)
				}
				_ = probe.Close()
			}
			if err := fixture.service.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			waitProductionMieru(t, fixture.inbound.Id)
			after, err := ledger.Read(t.Context(), record.PolicyID)
			if err != nil || after.Up != before.Up || after.Down != before.Down || after.Billed != before.Billed || after.Remainder != before.Remainder {
				t.Fatalf("core crash changed durable usage: before=%+v after=%+v error=%v", before, after, err)
			}
			openProductionMieruFlows(t, productionMieruClient(t, underlay, fixture.address, fixture.user))
			t.Logf("actual SIGKILL protected native %s and recovered with unchanged durable counters", underlay)
		})
	}
}
