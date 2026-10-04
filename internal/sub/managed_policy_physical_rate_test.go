package sub

import (
	"context"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func TestManagedPolicyTwoPhysicalNodesActualDirectionalRateAndBurst(t *testing.T) {
	fixture := newManagedPhysicalCase(t, model.ClientPolicyScopeGlobal, "1", 1048576, 8192, 16384)
	const perNode = 65536
	for _, direction := range []struct {
		marker byte
		rate   int64
	}{{'U', 8192}, {'D', 16384}} {
		elapsed := managedPhysicalRateTransfer(t, fixture.nodes, direction.marker, perNode)
		// The original global burst is 65536, split between the nodes. This
		// payload exceeds it and measures delivery at both real targets.
		minimum := time.Duration(float64(2*perNode-65536) / float64(direction.rate) * float64(time.Second))
		if elapsed < minimum*85/100 {
			t.Errorf("aggregate direction %c exceeded global rate/burst: elapsed=%s minimum=%s", direction.marker, elapsed, minimum)
		}
		t.Logf("physical direction=%c nodes=2 raw_bytes=%d global_rate=%d global_burst=65536 actual_elapsed=%s", direction.marker, 2*perNode, direction.rate, elapsed)
	}
	if err := service.StopManagedPolicyCoordinator(context.Background()); err != nil {
		t.Fatal("actual rate-limited grants did not seal", err)
	}
	page := managedProductRPC[service.ManagedPolicyAccountPage](t, fixture.product, "POST", "/accounts", service.ManagedPolicyAccountPageRequest{ParentClientID: fixture.parent.StableID, Limit: 16})
	if len(page.Accounts) != 1 || page.Accounts[0].Usage.Upload != "131076" || page.Accounts[0].Usage.Download != "131072" || page.Accounts[0].Usage.Billed != "262148" || page.Accounts[0].Budget.Allocated != "0" {
		t.Fatalf("real directional transfer did not settle exactly: %+v", page.Accounts)
	}
}
