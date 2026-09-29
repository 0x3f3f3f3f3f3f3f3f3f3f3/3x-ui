package policy_test

import (
	"context"
	"fmt"
	"net"
	"slices"
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func firstUsedTimestamp(t *testing.T, message proto.Message) int64 {
	t.Helper()
	reflected := message.ProtoReflect()
	field := reflected.Descriptor().Fields().ByName("first_used_at")
	if field == nil || field.Kind() != protoreflect.Int64Kind {
		t.Fatalf("private API does not export the first-use boundary: %s", reflected.Descriptor().FullName())
	}
	return reflected.Get(field).Int()
}

func TestFirstUseExpiryPrivateAPICarriesRealTrafficBoundary(t *testing.T) {
	socket := privateSocket(t)
	echo, listen := tcpEcho(t), port(t)
	start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1"]},"clientPolicy":{"policies":[{"clientId":"first-use-owner","version":1,"enabled":true,"multiplierMicros":1000000,"burstBytes":65536,"expiresAt":-300}]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"first-use-owner"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, socket, listen, echo.Addr().(*net.TCPAddr).Port))
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	api := command.NewClientPolicyServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	flow, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	time.Sleep(350 * time.Millisecond)
	before := time.Now().UnixMilli()
	exchange(t, flow, []byte{1, 2, 3, 4})
	after := time.Now().UnixMilli()
	if _, err := api.CheckpointUsage(ctx, &command.Empty{}); err != nil {
		t.Fatal(err)
	}
	ledger, err := api.ReadLedger(ctx, &command.LedgerRequest{Limit: 10})
	if err != nil || len(ledger.Records) != 1 {
		t.Fatalf("first-use ledger: %+v, %v", ledger, err)
	}
	stamp := firstUsedTimestamp(t, ledger.Records[0])
	if stamp < before || stamp > after || ledger.Records[0].Usage.RawUpload != 4 || ledger.Records[0].Usage.RawDownload != 4 {
		t.Fatalf("first-use boundary does not match admitted traffic: stamp=%d bounds=%d..%d ledger=%+v", stamp, before, after, ledger)
	}
	state, err := api.GetClient(ctx, &command.ClientRequest{ClientId: "first-use-owner"})
	if err != nil {
		t.Fatal(err)
	}
	if firstUsedTimestamp(t, state) != stamp {
		t.Fatalf("snapshot and ledger disagree about first use: %+v", state)
	}
	caps, err := api.GetCapabilities(ctx, &command.Empty{})
	if err != nil || !slices.Contains(caps.GetCapabilities(), "durable-first-use-expiry-v1") {
		t.Fatalf("missing explicit first-use capability: %+v, %v", caps, err)
	}
	_ = flow.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := flow.Read(make([]byte, 1)); err == nil {
		t.Fatal("first-use expiry did not close the real stream")
	} else if timed, ok := err.(net.Error); ok && timed.Timeout() {
		t.Fatal("first-use expiry left the real stream open")
	}
}
