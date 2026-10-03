package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestManagedAuthorityNodeDiscoveryUsesCurrentOwnedBoot(t *testing.T) {
	svc, inbound, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	before, err := owner.state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	node := &ClientPolicyNodeService{}
	first, err := node.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || first == nil || first.Capabilities == nil || first.Challenge == nil {
		t.Fatalf("actual owned authority discovery unavailable: %v", err)
	}
	if first.Capabilities.InstanceId != owner.state.SourceID || first.Capabilities.BootId != owner.socketBoot || first.Challenge.InstanceId != first.Capabilities.InstanceId || first.Challenge.BootId != owner.socketBoot || len(first.Challenge.ChallengeId) != 32 || first.Challenge.MaxDurationMillis != 10000 {
		t.Fatal("discovery did not identify the actual owned core challenge")
	}
	request := panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: first.Capabilities.InstanceId, ExpectedBootID: first.Capabilities.BootId}
	first.Capabilities.Capabilities[0] = "modified-response-only"
	next, err := node.DiscoverAuthority(ctx, request)
	if err != nil || next.Challenge.ChallengeId == first.Challenge.ChallengeId || next.Capabilities.Capabilities[0] == "modified-response-only" {
		t.Fatal("bound discovery reused its nonce or mutable capability storage")
	}
	after, err := owner.state.Journal.Account(client.StableID)
	if err != nil || after.Usage != before.Usage || after.HeldCapacity != before.HeldCapacity {
		t.Fatal("discovery changed billed usage or allocated quota")
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	if stale, err := node.DiscoverAuthority(ctx, request); err == nil || stale != nil {
		t.Fatal("discovery accepted the predecessor boot after real restart")
	}
	fresh, err := node.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || fresh.Capabilities.BootId == request.ExpectedBootID || fresh.Capabilities.InstanceId != request.ExpectedInstanceID {
		t.Fatal("restart discovery did not retain instance and replace boot")
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "live")
}

func TestManagedAuthorityNodeDiscoveryRejectsUnsafeOwnership(t *testing.T) {
	svc, _, _, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	node := &ClientPolicyNodeService{}
	reject := func(t *testing.T, ctx context.Context, request panelruntime.AuthorityDiscoveryRequest) {
		t.Helper()
		if result, err := node.DiscoverAuthority(ctx, request); err == nil || result != nil {
			t.Fatal("unsafe authority discovery produced a successful snapshot")
		}
	}
	for name, request := range map[string]panelruntime.AuthorityDiscoveryRequest{
		"partial-instance": {ExpectedInstanceID: owner.state.SourceID},
		"partial-boot":     {ExpectedBootID: owner.socketBoot},
		"wrong-instance":   {ExpectedInstanceID: "different-owned-instance", ExpectedBootID: owner.socketBoot},
		"wrong-boot":       {ExpectedInstanceID: owner.state.SourceID, ExpectedBootID: strings.Repeat("0", 32)},
		"invalid-boot":     {ExpectedInstanceID: owner.state.SourceID, ExpectedBootID: "invalid"},
	} {
		t.Run(name, func(t *testing.T) { reject(t, context.Background(), request) })
	}
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reject(t, ctx, panelruntime.AuthorityDiscoveryRequest{})
	})
	t.Run("nil-context", func(t *testing.T) { reject(t, nil, panelruntime.AuthorityDiscoveryRequest{}) })
	t.Run("foreign-process-reference", func(t *testing.T) {
		restore := SetXrayProcessForTest(&panelxray.Process{})
		defer restore()
		reject(t, context.Background(), panelruntime.AuthorityDiscoveryRequest{})
	})
	t.Run("active-restore", func(t *testing.T) {
		lease, err := acquireDatabaseRestore()
		if err != nil {
			t.Fatal(err)
		}
		defer lease.release()
		reject(t, context.Background(), panelruntime.AuthorityDiscoveryRequest{})
	})
	t.Run("sql-restore-admission", func(t *testing.T) {
		lease, err := database.BeginRestore()
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		reject(t, context.Background(), panelruntime.AuthorityDiscoveryRequest{})
	})
	t.Run("changed-private-socket", func(t *testing.T) {
		retained := owner.socketPath + ".discovery-owned"
		if err := os.Rename(owner.socketPath, retained); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Rename(retained, owner.socketPath); err != nil {
				t.Fatal(err)
			}
		}()
		listener, err := net.Listen("unix", owner.socketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if err := os.Chmod(owner.socketPath, 0600); err != nil {
			t.Fatal(err)
		}
		reject(t, context.Background(), panelruntime.AuthorityDiscoveryRequest{})
	})
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	t.Run("stopped", func(t *testing.T) { reject(t, context.Background(), panelruntime.AuthorityDiscoveryRequest{}) })
}
