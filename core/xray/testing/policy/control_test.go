package policy_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"

	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func privateSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cpctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "control.sock")
}

func TestPrivateControlAPIChangesExistingFlowAndExportsCommittedLedger(t *testing.T) {
	socket := privateSocket(t)
	echo := tcpEcho(t)
	listen := port(t)
	instance := start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1"]},"clientPolicy":{"policies":[{"clientId":"api-owner","version":1,"enabled":true,"multiplierMicros":1000000,"burstBytes":65536,"uploadBytesPerSecond":1}]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"api-owner"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, socket, listen, echo.Addr().(*net.TCPAddr).Port))
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	api := command.NewClientPolicyServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	caps, err := api.GetCapabilities(ctx, &command.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if caps.ApiVersion != 1 || caps.InstanceId != "test-node" || caps.ReservationRawBytes != 65536 {
		t.Fatalf("wrong capability contract: %+v", caps)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("control socket permissions: %v %v", info, err)
	}
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	exchange(t, c, bytes.Repeat([]byte{1}, 65536))
	payload := bytes.Repeat([]byte{2}, 8192)
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(c, reply); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("limited existing stream did not block: %v", err)
	case <-time.After(120 * time.Millisecond):
	}
	changed := &clientpolicy.PolicyConfig{ClientId: "api-owner", Version: 2, Enabled: true, MultiplierMicros: 2000000, BurstBytes: 65536}
	began := time.Now()
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	applyFixturePolicyRPC(t, ctx, api, engine, changed)
	select {
	case err := <-done:
		if err != nil || !bytes.Equal(reply, payload) {
			t.Fatalf("hot update corrupted flow: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hot update exceeded 2 seconds")
	}
	if time.Since(began) > 2*time.Second {
		t.Fatal("hot update exceeded 2 seconds")
	}
	if _, err := api.CheckpointUsage(ctx, &command.Empty{}); err != nil {
		t.Fatal(err)
	}
	ledger, err := api.ReadLedger(ctx, &command.LedgerRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Records) != 1 || ledger.Records[0].Usage.RawUpload != 73728 || ledger.Records[0].Usage.RawDownload != 73728 || ledger.Records[0].Usage.BilledBytes != 163840 || ledger.Records[0].PolicyVersion != 2 {
		t.Fatalf("wrong versioned ledger: %+v", ledger)
	}
	connections, err := api.ListConnections(ctx, &command.ClientRequest{ClientId: "api-owner"})
	if err != nil || len(connections.Connections) != 1 || connections.Connections[0].InboundTag != "owned" {
		t.Fatalf("missing active identity: %+v %v", connections, err)
	}
	changed.Version = 1
	if _, err := api.ApplyPolicies(ctx, &command.ApplyRequest{Policies: []*clientpolicy.PolicyConfig{changed}}); status.Code(err) != codes.Aborted {
		t.Fatalf("accepted stale policy: %v", err)
	}
	closed, err := api.CloseConnections(ctx, &command.ClientRequest{ClientId: "api-owner"})
	if err != nil || closed.Closed != 1 {
		t.Fatalf("close result: %+v %v", closed, err)
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("control API did not close TCP connection")
	} else if n, ok := err.(net.Error); ok && n.Timeout() {
		t.Fatal("control API left socket open")
	}
}

func TestManagedControlServiceRejectsTCPAndAbstractSockets(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:0", "0.0.0.0:0", "@unprotected-policy", ""} {
		api := &conf.APIConfig{Tag: "control", Listen: listen, Services: []string{"ClientPolicyServiceV1"}}
		if _, err := api.Build(); err == nil {
			t.Fatalf("accepted unprotected policy API at %q", listen)
		}
	}
}

func TestPrivateControlInitializesLegacyUsageOnce(t *testing.T) {
	socket := privateSocket(t)
	start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1"]},"clientPolicy":{"policies":[]},"outbounds":[{"protocol":"blackhole"}]}`, socket))
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	api := command.NewClientPolicyServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := &command.InitializeRequest{Policy: &clientpolicy.PolicyConfig{ClientId: "migrated", Version: 1, Enabled: true, MultiplierMicros: 2000000, QuotaBytes: 100, BurstBytes: 65536}, Usage: &command.Usage{RawUpload: 10, RawDownload: 20, BilledBytes: 30}}
	for range 2 {
		if _, err := api.InitializeClient(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	state, err := api.GetClient(ctx, &command.ClientRequest{ClientId: "migrated"})
	if err != nil || state.Usage.BilledBytes != 30 || state.Usage.RawUpload != 10 || state.Usage.RawDownload != 20 || state.Policy.MultiplierMicros != 2000000 {
		t.Fatalf("incorrect seed: %v/%v", state, err)
	}
	request.Usage.BilledBytes = 0
	if _, err := api.InitializeClient(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("accepted conflicting seed: %v", err)
	}
}
