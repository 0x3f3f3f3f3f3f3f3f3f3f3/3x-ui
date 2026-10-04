package sub

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// Removing lease enforcement, returning uncertain allowance, reseeding the
// reopened coordinator, or multiplying replayed receipts must fail this owner.
func TestManagedPolicyTwoPhysicalNodesActualPartitionExpiryAndCoordinatorRecovery(t *testing.T) {
	fixture := newManagedPhysicalCase(t, model.ClientPolicyScopeGlobal, "1.5", 8192, 0, 0)
	flows := managedPhysicalFundedFlows(t, fixture, 1)
	for _, flow := range flows {
		sshHTTPEcho(t, flow, "w")
	}
	before := managedPhysicalWaitAccount(t, fixture, "6", "8186")
	for _, node := range fixture.nodes {
		managedPhysicalControl(t, node, "partition")
	}
	if err := service.StopManagedPolicyCoordinator(context.Background()); err == nil {
		t.Fatal("unavailable real node seal was acknowledged")
	}
	// These are real lease deadlines, with no substituted clock or fake grant.
	timer := time.NewTimer(policyauthority.MaxLeaseDuration + 250*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	for _, flow := range flows {
		sshHTTPClosed(t, flow)
	}
	after := managedPhysicalWaitAccount(t, fixture, "6", "8186")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partition, expiry or original reopen changed uncertain balance")
	}
	for _, node := range fixture.nodes {
		managedPhysicalControl(t, node, "rejoin")
	}
	if err := service.ResumeManagedPolicyCoordinator(context.Background()); err != nil {
		t.Fatal("authentic original recovery failed", err)
	}
	if err := service.StopManagedPolicyCoordinator(context.Background()); err != nil {
		t.Fatal("recovered real seals failed", err)
	}
	sealed := managedPhysicalWaitAccount(t, fixture, "6", "0")
	if sealed.Usage.Upload != "2" || sealed.Usage.Download != "2" || sealed.Usage.Billed != "6" || sealed.Remaining == nil || *sealed.Remaining != "8186" || sealed.Budget.Unallocated == nil || *sealed.Budget.Unallocated != "8186" {
		t.Fatalf("authentic recovery rebilled or lost allowance: %+v", sealed)
	}
	if err := service.ResumeManagedPolicyCoordinator(context.Background()); err != nil {
		t.Fatal(err)
	}
	managedPhysicalExchange(t, fixture.nodes[0].manifest.TunnelPort, "tcp4", []byte("r"), true)
	if err := service.StopManagedPolicyCoordinator(context.Background()); err != nil {
		t.Fatal(err)
	}
	final := managedPhysicalWaitAccount(t, fixture, "9", "0")
	if final.Usage.Upload != "3" || final.Usage.Download != "3" || final.Usage.Billed != "9" {
		t.Fatalf("post-recovery payload did not settle once: %+v", final)
	}
}
