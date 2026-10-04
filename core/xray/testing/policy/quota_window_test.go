package policy_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/testing/testauthority"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestQuotaWindowConfigAndPrivateAPIKeepRealTunnelHistory(t *testing.T) {
	socket := privateSocket(t)
	state := filepath.Join(filepath.Dir(socket), "state.db")
	if err := clientpolicy.CreateStore(state, "quota-window"); err != nil {
		t.Fatal(err)
	}
	e, err := clientpolicy.OpenPersistentEngine(state, "quota-window")
	if err != nil {
		t.Fatal(err)
	}
	p := clientpolicy.Policy{ClientID: "owner", Version: 1, Enabled: true, Multiplier: 500000, BurstBytes: 65536, QuotaBytes: 2}
	if err := e.Initialize(p, clientpolicy.Usage{RawUpload: 201, BilledBytes: 100, Remainder: 500000}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	echo, listen := tcpEcho(t), port(t)
	instance := start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1"]},"clientPolicy":{"stateFile":%q,"instanceId":"quota-window","policies":[{"clientId":"owner","version":2,"enabled":true,"multiplierMicros":500000,"burstBytes":65536,"quotaBytes":2,"quotaBaselineBytes":100,"quotaBaselineRemainder":500000}]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"owner"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, socket, state, listen, echo.Addr().(*net.TCPAddr).Port))
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	exchange(t, c, []byte{1})
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	api := command.NewClientPolicyServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	changed := &clientpolicy.PolicyConfig{ClientId: "owner", Version: 3, Enabled: true, MultiplierMicros: 500000, BurstBytes: 65536, QuotaBytes: 2, QuotaBaselineBytes: 101, QuotaBaselineRemainder: 500000}
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	applyFixturePolicyRPC(t, ctx, api, engine, changed)
	exchange(t, c, []byte{2})
	got, err := api.GetClient(ctx, &command.ClientRequest{ClientId: "owner"})
	if err != nil || got.Policy.QuotaBaselineBytes != 101 || got.Policy.QuotaBaselineRemainder != 500000 || got.Usage.RawUpload != 203 || got.Usage.RawDownload != 2 || got.Usage.BilledBytes != 102 || got.Usage.Remainder != 500000 {
		t.Fatalf("private reset lost lifetime history or baseline: %+v %v", got, err)
	}
	changed.Version++
	changed.QuotaBaselineBytes = 103
	if _, err := api.ApplyPolicies(ctx, &command.ApplyRequest{Policies: []*clientpolicy.PolicyConfig{changed}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("RPC accepted future baseline: %v", err)
	}
	changed.ClientId, changed.Version, changed.QuotaBaselineBytes = "seeded", 1, 100
	if _, err := api.InitializeClient(ctx, &command.InitializeRequest{Policy: changed, Usage: &command.Usage{RawUpload: 201, BilledBytes: 100, Remainder: 500000}}); err != nil {
		t.Fatal(err)
	}
	got, err = api.GetClient(ctx, &command.ClientRequest{ClientId: "seeded"})
	if err != nil || got.Reasons != uint32(clientpolicy.ReasonAuthority) {
		t.Fatalf("seeded usage invented execution authority: %+v/%v", got, err)
	}
	seeded, _, err := engine.GetClient("seeded")
	if err != nil {
		t.Fatal(err)
	}
	testauthority.Grant(t, engine, seeded)
	got, err = api.GetClient(ctx, &command.ClientRequest{ClientId: "seeded"})
	if err != nil || got.Reasons != 0 || got.Policy.QuotaBaselineBytes != 100 || got.Policy.QuotaBaselineRemainder != 500000 {
		t.Fatalf("initialization discarded the quota baseline: %+v %v", got, err)
	}
}
