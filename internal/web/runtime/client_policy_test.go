package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestManagedMissingClientCannotRestartConsumedRelativeExpiry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		seed, floor *command.Usage
	}{
		{"protected-floor", &command.Usage{}, &command.Usage{RawUpload: 4, BilledBytes: 4}},
		{"historical-seed", &command.Usage{RawDownload: 4, BilledBytes: 4}, nil},
		{"fractional-floor", &command.Usage{}, &command.Usage{Remainder: 500000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := deletionPolicyAPI(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			policy := &clientpolicy.PolicyConfig{ClientId: "lost-relative", Version: 1, Enabled: true, MultiplierMicros: 1000000, BurstBytes: 65536, ExpiresAt: -60000}
			bootstrap := &ManagedPolicyBootstrap{Initializations: []*command.InitializeRequest{{Policy: policy, Usage: tc.seed}}, UsageFloors: map[string]*command.Usage{policy.ClientId: tc.floor}}
			if err := initializeManagedClients(ctx, api, bootstrap); !errors.Is(err, clientpolicy.ErrAuthority) {
				t.Fatalf("missing original first-use time allowed relative lifetime recovery: %v", err)
			}
			if state, err := api.GetClient(ctx, policy.ClientId); status.Code(err) != codes.NotFound {
				t.Fatalf("rejected recovery created an activatable client: %+v %v", state, err)
			}
		})
	}
}

func TestManagedMissingClientInitializesFreshRelativeAndHistoricalAbsoluteExpiry(t *testing.T) {
	api := deletionPolicyAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, tc := range []struct {
		id        string
		expiresAt int64
		floor     *command.Usage
	}{
		{"fresh-relative", -60000, &command.Usage{}},
		{"historical-absolute", time.Now().Add(time.Hour).UnixMilli(), &command.Usage{RawUpload: 4, BilledBytes: 4, Remainder: 500000}},
	} {
		policy := &clientpolicy.PolicyConfig{ClientId: tc.id, Version: 1, Enabled: true, MultiplierMicros: 1000000, BurstBytes: 65536, ExpiresAt: tc.expiresAt}
		bootstrap := &ManagedPolicyBootstrap{Initializations: []*command.InitializeRequest{{Policy: policy, Usage: &command.Usage{}}}, UsageFloors: map[string]*command.Usage{tc.id: tc.floor}}
		if err := initializeManagedClients(ctx, api, bootstrap); err != nil {
			t.Fatal(err)
		}
		state, err := api.GetClient(ctx, tc.id)
		if err != nil || !managedUsageAtLeast(state.GetUsage(), tc.floor) {
			t.Fatalf("valid initialization lost the usage floor: %+v %v", state, err)
		}
	}
}
