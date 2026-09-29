package xray

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/xtls/xray-core/app/clientpolicy"
	policycommand "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/app/commander"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var ErrClientPolicyCapability = errors.New("core does not provide the required custom client-policy capability")

type ClientPolicyAPI struct {
	conn         *grpc.ClientConn
	client       policycommand.ClientPolicyServiceClient
	capabilities *policycommand.Capabilities
}

func DialClientPolicy(ctx context.Context, socket, expectedInstance string) (*ClientPolicyAPI, error) {
	if expectedInstance == "" {
		return nil, fmt.Errorf("%w: expected instance ID is required", ErrClientPolicyCapability)
	}
	if err := commander.ValidatePrivateUnixSocket(socket); err != nil {
		return nil, err
	}
	info, err := os.Stat(socket)
	if err != nil {
		return nil, fmt.Errorf("client policy socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("client policy socket must be private")
	}
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	client := policycommand.NewClientPolicyServiceClient(conn)
	capabilities, err := client.GetCapabilities(ctx, &policycommand.Empty{})
	if err != nil {
		conn.Close()
		if status.Code(err) == codes.Unimplemented {
			return nil, fmt.Errorf("%w: service unavailable", ErrClientPolicyCapability)
		}
		return nil, fmt.Errorf("client policy capability query: %w", err)
	}
	if capabilities.ApiVersion != 1 || capabilities.InstanceId != expectedInstance {
		conn.Close()
		return nil, fmt.Errorf("%w: API version or instance identity mismatch", ErrClientPolicyCapability)
	}
	present := make(map[string]bool, len(capabilities.Capabilities))
	for _, name := range capabilities.Capabilities {
		present[name] = true
	}
	for _, name := range []string{"trusted-tunnel-client-id-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "live-session-control-v1", "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1"} {
		if !present[name] {
			conn.Close()
			return nil, fmt.Errorf("%w: missing %s", ErrClientPolicyCapability, name)
		}
	}
	return &ClientPolicyAPI{conn: conn, client: client, capabilities: capabilities}, nil
}

func (c *ClientPolicyAPI) Close() error { return c.conn.Close() }
func (c *ClientPolicyAPI) Capabilities() *policycommand.Capabilities {
	return proto.Clone(c.capabilities).(*policycommand.Capabilities)
}

func (c *ClientPolicyAPI) GetClient(ctx context.Context, id string) (*policycommand.ClientState, error) {
	return c.client.GetClient(ctx, &policycommand.ClientRequest{ClientId: id})
}

func (c *ClientPolicyAPI) Initialize(ctx context.Context, policy *clientpolicy.PolicyConfig, seed *policycommand.Usage) error {
	_, err := c.client.InitializeClient(ctx, &policycommand.InitializeRequest{Policy: policy, Usage: seed})
	return err
}

func (c *ClientPolicyAPI) Apply(ctx context.Context, policies []*clientpolicy.PolicyConfig) error {
	_, err := c.client.ApplyPolicies(ctx, &policycommand.ApplyRequest{Policies: policies})
	return err
}

func (c *ClientPolicyAPI) Revoke(ctx context.Context, id string, version uint64) error {
	_, err := c.client.RevokeClient(ctx, &policycommand.ClientRequest{ClientId: id, ExpectedPolicyVersion: version})
	return err
}

func (c *ClientPolicyAPI) Connections(ctx context.Context, id string) (*policycommand.Connections, error) {
	return c.client.ListConnections(ctx, &policycommand.ClientRequest{ClientId: id})
}

func (c *ClientPolicyAPI) CloseConnections(ctx context.Context, id string) (uint32, error) {
	r, err := c.client.CloseConnections(ctx, &policycommand.ClientRequest{ClientId: id})
	if err != nil {
		return 0, err
	}
	return r.Closed, nil
}

func (c *ClientPolicyAPI) Checkpoint(ctx context.Context) error {
	_, err := c.client.CheckpointUsage(ctx, &policycommand.Empty{})
	return err
}

func (c *ClientPolicyAPI) ReadLedger(ctx context.Context, after uint64, limit uint32) (*policycommand.LedgerPage, error) {
	return c.client.ReadLedger(ctx, &policycommand.LedgerRequest{AfterSequence: after, Limit: limit})
}
