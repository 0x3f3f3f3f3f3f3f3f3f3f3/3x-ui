package xray

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	policycommand "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/grpc"
)

type capabilityServer struct {
	policycommand.UnimplementedClientPolicyServiceServer
	response *policycommand.Capabilities
}

func (s *capabilityServer) GetCapabilities(context.Context, *policycommand.Empty) (*policycommand.Capabilities, error) {
	return s.response, nil
}

func capabilitySocket(t *testing.T, response *policycommand.Capabilities) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "panel-cp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "core.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := grpc.NewServer()
	if response != nil {
		policycommand.RegisterClientPolicyServiceServer(server, &capabilityServer{response: response})
	}
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	return path
}

func TestClientPolicyAdapterRejectsMissingOrIncompatibleCoreCapabilities(t *testing.T) {
	for _, test := range []struct {
		name string
		caps *policycommand.Capabilities
	}{
		{name: "official-core-without-service"},
		{name: "wrong-api-version", caps: &policycommand.Capabilities{ApiVersion: 2, InstanceId: "expected"}},
		{name: "wrong-core-instance", caps: &policycommand.Capabilities{ApiVersion: 1, InstanceId: "different"}},
		{name: "missing-enforcement", caps: &policycommand.Capabilities{ApiVersion: 1, InstanceId: "expected"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := capabilitySocket(t, test.caps)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			client, err := DialClientPolicy(ctx, path, "expected")
			if err == nil {
				client.Close()
				t.Fatal("accepted unsupported core")
			}
			if !errors.Is(err, ErrClientPolicyCapability) {
				t.Fatalf("capability mismatch not explicit: %v", err)
			}
		})
	}
}

func TestClientPolicyAdapterNegotiatesVersionAndStableInstance(t *testing.T) {
	caps := &policycommand.Capabilities{ApiVersion: 1, InstanceId: "expected", Epoch: 4, Capabilities: []string{"trusted-tunnel-client-id-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "live-session-control-v1", "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1"}}
	path := capabilitySocket(t, caps)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := DialClientPolicy(ctx, path, "expected")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if got := client.Capabilities(); got.InstanceId != "expected" || got.Epoch != 4 {
		t.Fatalf("negotiation lost identity: %+v", got)
	}
	mutated := client.Capabilities()
	mutated.InstanceId = "untrusted"
	if client.Capabilities().InstanceId != "expected" {
		t.Fatal("caller mutated trusted capability result")
	}
}
