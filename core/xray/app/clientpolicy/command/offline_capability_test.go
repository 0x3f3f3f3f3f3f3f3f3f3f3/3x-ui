package command

import (
	"context"
	"slices"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCompiledCapabilitiesCoverNativePolicyWithoutInstanceClaims(t *testing.T) {
	features := BuiltinCapabilities()
	for _, required := range []string{"trusted-snell-client-id-v1", "trusted-mieru-client-id-v1", "trusted-ssh-client-id-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "quota-window-baseline-v1", "trusted-tunnel-client-id-v1", "tunnel-source-acl-v1", "tunnel-fixed-outbound-v1"} {
		if !slices.Contains(features, required) {
			t.Fatalf("compiled package lacks required capability %s", required)
		}
	}
	for _, instanceOnly := range []string{"local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1", "durable-first-use-expiry-v1"} {
		if slices.Contains(features, instanceOnly) {
			t.Fatalf("offline report claims configured instance durability: %s", instanceOnly)
		}
	}
	features[0] = "changed-caller-copy"
	if slices.Contains(BuiltinCapabilities(), "changed-caller-copy") {
		t.Fatal("a caller changed future compiled capability reports")
	}
	engine := clientpolicy.NewEngine()
	t.Cleanup(func() { _ = engine.Close() })
	_, err := (&service{engine: engine}).GetCapabilities(context.Background(), &Empty{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("offline report bypassed live RPC authorization: %v", err)
	}
}
