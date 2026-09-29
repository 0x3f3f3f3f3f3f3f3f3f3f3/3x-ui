package xray

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	policycommand "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"google.golang.org/grpc"
)

type managedMutationProbe struct {
	command.UnimplementedHandlerServiceServer
	calls atomic.Int32
	users chan *protocol.User
}

func (s *managedMutationProbe) AlterInbound(_ context.Context, req *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	s.calls.Add(1)
	op, err := req.Operation.GetInstance()
	if err != nil {
		return nil, err
	}
	if add, ok := op.(*command.AddUserOperation); ok {
		s.users <- add.User
	}
	return &command.AlterInboundResponse{}, nil
}

func (s *managedMutationProbe) AddInbound(context.Context, *command.AddInboundRequest) (*command.AddInboundResponse, error) {
	s.calls.Add(1)
	return &command.AddInboundResponse{}, nil
}

func managedMutationAPI(t *testing.T, caps *policycommand.Capabilities, useTCP bool) (*XrayAPI, *managedMutationProbe) {
	t.Helper()
	dir, err := os.MkdirTemp("", "managed-mutation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "core.sock")
	network := "unix"
	if useTCP {
		network, path = "tcp", "127.0.0.1:0"
	}
	listener, err := net.Listen(network, path)
	if err != nil {
		t.Fatal(err)
	}
	if useTCP {
		path = listener.Addr().String()
	} else {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = listener.Close()
			t.Fatal(err)
		}
	}
	server := grpc.NewServer()
	probe := &managedMutationProbe{users: make(chan *protocol.User, 8)}
	command.RegisterHandlerServiceServer(server, probe)
	if caps != nil {
		policycommand.RegisterClientPolicyServiceServer(server, &capabilityServer{response: caps})
	}
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	api := new(XrayAPI)
	if err := api.InitEndpoint(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)
	return api, probe
}

func TestManagedMutationRequiresCapabilitiesBeforeChangingHandlers(t *testing.T) {
	api, probe := managedMutationAPI(t, nil, false)
	user := map[string]any{"email": "managed", "id": "936997e1-3b0c-4de9-9eea-047ee5829d3e", "clientId": "owner"}
	if err := api.AddUser("vless", "owned", user); !errors.Is(err, ErrClientPolicyCapability) {
		t.Fatalf("managed user accepted an unmodified control service: %v", err)
	}
	for _, inbound := range []string{
		`{"tag":"owned","listen":"127.0.0.1","port":12345,"protocol":"tunnel","settings":{"address":"127.0.0.1","port":12346,"network":"tcp","clientId":"owner"}}`,
		`{"tag":"owned","listen":"127.0.0.1","port":12345,"protocol":"vless","settings":{"decryption":"none","clients":[{"id":"936997e1-3b0c-4de9-9eea-047ee5829d3e","email":"managed","clientId":"owner"}]}}`,
	} {
		if err := api.AddInbound([]byte(inbound)); !errors.Is(err, ErrClientPolicyCapability) {
			t.Fatalf("managed inbound accepted an unmodified control service: %v", err)
		}
	}
	if probe.calls.Load() != 0 {
		t.Fatal("capability rejection mutated a handler")
	}
	delete(user, "clientId")
	if err := api.AddUser("vless", "legacy", user); err != nil {
		t.Fatalf("legacy user unnecessarily requires managed capabilities: %v", err)
	}
}

func TestManagedMutationPreservesStableUserIdentity(t *testing.T) {
	caps := &policycommand.Capabilities{ApiVersion: 1, InstanceId: "node", Epoch: 1, Capabilities: []string{"trusted-tunnel-client-id-v1", "trusted-vless-client-id-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "live-session-control-v1", "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1"}}
	api, probe := managedMutationAPI(t, caps, false)
	user := map[string]any{"email": "display-name", "id": "936997e1-3b0c-4de9-9eea-047ee5829d3e", "clientId": "owner"}
	if err := api.AddUser("vless", "owned", user); err != nil {
		t.Fatal(err)
	}
	got := <-probe.users
	if got.ClientId != "owner" || got.Email != "display-name" {
		t.Fatalf("handler request lost stable identity: %+v", got)
	}
	user["clientId"] = 123
	if err := api.AddUser("vless", "owned", user); err == nil {
		t.Fatal("invalid identity silently became unmanaged")
	}
	if probe.calls.Load() != 1 {
		t.Fatal("invalid identity mutated a handler")
	}
	if err := api.AddInbound([]byte(`{"tag":"owned","listen":"127.0.0.1","port":12345,"protocol":"vless","settings":{"decryption":"none","clients":[{"id":"936997e1-3b0c-4de9-9eea-047ee5829d3e","email":"managed","clientId":"owner"}]}}`)); err != nil {
		t.Fatalf("compatible core rejected a managed inbound: %v", err)
	}
	if probe.calls.Load() != 2 {
		t.Fatal("compatible managed inbound did not reach the handler")
	}
}

func TestManagedMutationRejectsMissingProtocolCapabilityAndUnsupportedAccount(t *testing.T) {
	caps := &policycommand.Capabilities{ApiVersion: 1, InstanceId: "node", Epoch: 1, Capabilities: []string{"trusted-tunnel-client-id-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "live-session-control-v1", "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1"}}
	api, probe := managedMutationAPI(t, caps, false)
	for _, name := range []string{"vless", "tunnel"} {
		user := map[string]any{"email": "managed", "id": "936997e1-3b0c-4de9-9eea-047ee5829d3e", "clientId": "owner"}
		if err := api.AddUser(name, "owned", user); !errors.Is(err, ErrClientPolicyCapability) {
			t.Errorf("unsupported managed %s account was accepted: %v", name, err)
		}
	}
	if err := api.AddInbound([]byte(`{"tag":"owned","listen":"127.0.0.1","port":12345,"protocol":"vless","settings":{"decryption":"none","clients":[{"id":"936997e1-3b0c-4de9-9eea-047ee5829d3e","email":"managed","clientId":"owner"}]}}`)); !errors.Is(err, ErrClientPolicyCapability) {
		t.Errorf("core without authenticated protocol capability accepted an inbound: %v", err)
	}
	if probe.calls.Load() != 0 {
		t.Fatal("unsupported managed account altered the core")
	}
}

func TestManagedMutationRequiresPrivateControl(t *testing.T) {
	caps := &policycommand.Capabilities{ApiVersion: 1, InstanceId: "node", Epoch: 1, Capabilities: []string{"trusted-tunnel-client-id-v1", "trusted-vless-client-id-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "live-session-control-v1", "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1"}}
	api, probe := managedMutationAPI(t, caps, true)
	if err := api.AddUser("vless", "owned", map[string]any{"email": "managed", "id": "936997e1-3b0c-4de9-9eea-047ee5829d3e", "clientId": "owner"}); !errors.Is(err, ErrClientPolicyCapability) {
		t.Fatalf("managed mutation accepted a TCP control endpoint: %v", err)
	}
	if probe.calls.Load() != 0 {
		t.Fatal("managed mutation reached TCP control")
	}
}
