package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestManagedDeletionPagesPastUnknownIdentities(t *testing.T) {
	api := deletionPolicyAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := api.Apply(ctx, []*clientpolicy.PolicyConfig{{ClientId: "z-live", Version: 1, Enabled: true, MultiplierMicros: 1000000, BurstBytes: 65536}}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 1001)
	for i := range 1000 {
		ids[i] = fmt.Sprintf("absent-%04d", i)
	}
	ids[1000] = "z-live"
	local := NewLocal(LocalDeps{})
	var confirmed []string
	bootstrap := &ManagedPolicyBootstrap{
		DeletedClientPage: func(_ context.Context, after string) ([]string, error) {
			if !local.mu.TryLock() {
				t.Fatal("SQL page provider runs under Runtime lock")
			}
			local.mu.Unlock()
			start, found := slices.BinarySearch(ids, after)
			if found {
				start++
			}
			return ids[start:min(start+1000, len(ids))], nil
		},
		ConfirmAbsentClients: func(_ context.Context, ids []string) error {
			if !local.mu.TryLock() {
				t.Fatal("SQL confirmation runs under Runtime lock")
			}
			local.mu.Unlock()
			confirmed = append(confirmed, ids...)
			return nil
		},
	}
	if err := local.prepareManagedDeletions(ctx, api, bootstrap, nil); err != nil {
		t.Fatal(err)
	}
	state, err := api.GetClient(ctx, "z-live")
	if err != nil || state.Reasons&uint32(clientpolicy.ReasonRevoked) == 0 {
		t.Fatalf("unknown first page starved a live identity: %+v %v", state, err)
	}
	if !slices.Equal(confirmed, ids[:1000]) {
		t.Fatalf("absence confirmation included known identity or lost page: count=%d", len(confirmed))
	}
}

func TestManagedDeletionRejectsStaleBootstrapBeforeAbsenceConfirmation(t *testing.T) {
	api := deletionPolicyAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprintf("initial-%t", initial), func(t *testing.T) {
			confirmed := false
			bootstrap := &ManagedPolicyBootstrap{
				DeletedClientPage: func(_ context.Context, after string) ([]string, error) {
					if after == "" {
						return []string{"deleted"}, nil
					}
					return nil, nil
				},
				ConfirmAbsentClients: func(context.Context, []string) error { confirmed = true; return nil },
			}
			var policies []clientpolicy.Policy
			if initial {
				bootstrap.Initializations = []*command.InitializeRequest{{Policy: &clientpolicy.PolicyConfig{ClientId: "deleted", Version: 1, Enabled: true, MultiplierMicros: 1000000}, Usage: &command.Usage{}}}
			} else {
				policies = []clientpolicy.Policy{{ClientID: "deleted"}}
			}
			local := NewLocal(LocalDeps{})
			if err := local.prepareManagedDeletions(ctx, api, bootstrap, policies); !errors.Is(err, clientpolicy.ErrRevoked) {
				t.Fatalf("stale candidate retained deleted identity: %v", err)
			}
			if confirmed {
				t.Fatal("stale initialization permanently hid deletion work")
			}
			if _, err := api.GetClient(ctx, "deleted"); status.Code(err) != codes.NotFound {
				t.Fatalf("stale initialization reached core: %v", err)
			}
		})
	}
}

func deletionPolicyAPI(t *testing.T) *xray.ClientPolicyAPI {
	t.Helper()
	dir, err := os.MkdirTemp("", "deletion-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket, state := filepath.Join(dir, "core.sock"), filepath.Join(dir, "state.db")
	if err := clientpolicy.CreateStore(state, "deletion-test"); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"private","listen":%q,"services":["ClientPolicyServiceV1"]},"clientPolicy":{"stateFile":%q,"instanceId":"deletion-test"}}`, socket, state)
	var cfg conf.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	built, err := cfg.Build()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.New(built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	api, err := xray.DialClientPolicy(ctx, socket, "deletion-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	return api
}
