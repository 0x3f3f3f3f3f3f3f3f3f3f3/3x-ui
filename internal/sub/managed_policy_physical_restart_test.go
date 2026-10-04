package sub

import (
	"context"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// Returning the retired boot's unsealed credit, inventing its receipt, or
// resetting usage while a physical core restarts must fail this owner.
func TestManagedPolicyTwoPhysicalNodesActualRestartRetainsUnsealedBudget(t *testing.T) {
	fixture := newManagedPhysicalCase(t, model.ClientPolicyScopeGlobal, "1.5", 8192, 0, 0)
	for _, flow := range managedPhysicalFundedFlows(t, fixture, 1) {
		sshHTTPEcho(t, flow, "w")
		_ = flow.Close()
	}
	before := managedPhysicalWaitAccount(t, fixture, "6", "8186")
	old := fixture.nodes[0].manifest
	managedPhysicalControl(t, fixture.nodes[0], "restart")
	current := fixture.nodes[0].manifest
	if old.BootID == current.BootID || old.SourceID != current.SourceID || old.LocalClientID != current.LocalClientID || current.LocalPolicyVersion != "1" || old.PID != current.PID {
		t.Fatal("actual core restart lost source or account identity")
	}
	if err := service.ResumeManagedPolicyCoordinator(context.Background()); err != nil {
		t.Fatal("actual changed-boot reconnection failed", err)
	}
	after := managedPhysicalWaitAccount(t, fixture, "6", "8186")
	if before.Usage != after.Usage || before.Budget.Allocated != after.Budget.Allocated {
		t.Fatal("new boot replenished held credit or duplicated usage")
	}
	managedPhysicalDenied(t, fixture.nodes[0].manifest.TunnelPort)
	managedPhysicalExchange(t, fixture.nodes[1].manifest.TunnelPort, "tcp4", []byte("b"), true)
	if err := service.StopManagedPolicyCoordinator(context.Background()); err != nil {
		t.Fatal("remaining current-boot settlement failed", err)
	}
	final := managedPhysicalWaitAccount(t, fixture, "9", "4093")
	if final.Usage.Upload != "3" || final.Usage.Download != "3" || final.Usage.Billed != "9" || final.Budget.Unallocated == nil || *final.Budget.Unallocated != "4090" {
		t.Fatalf("new boot lost original uncertain allowance: %+v", final)
	}
}
