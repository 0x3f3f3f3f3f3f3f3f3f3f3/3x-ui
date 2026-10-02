package policy_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestPrivateAuthorityGrantBoundsRealTCPAndUDPTunnel(t *testing.T) {
	echo := tcpEcho(t)
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		b := make([]byte, 65535)
		for {
			n, peer, err := udp.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(b[:n], peer)
		}
	}()
	socket := privateSocket(t)
	tcpPort, udpPort := port(t), port(t)
	startWithoutAuthority(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1"]},"clientPolicy":{"policies":[{"clientId":"owner","version":1,"enabled":true,"multiplierMicros":1500000,"uploadBytesPerSecond":8192,"downloadBytesPerSecond":8192,"burstBytes":512}]},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"owner"}},{"listen":"127.0.0.1","port":%d,"protocol":"dokodemo-door","settings":{"network":"udp","address":"127.0.0.1","port":%d,"clientId":"owner"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, socket, tcpPort, echo.Addr().(*net.TCPAddr).Port, udpPort, udp.LocalAddr().(*net.UDPAddr).Port))
	connection, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	api := command.NewClientPolicyServiceClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	caps, err := api.GetCapabilities(ctx, &command.Empty{})
	if err != nil || caps.GetBootId() == "" || !slices.Contains(caps.GetCapabilities(), "boot-bound-execution-grants-v1") {
		t.Fatalf("grant capability missing: %+v/%v", caps, err)
	}
	binding := &command.AuthorityBinding{AuthorityId: "issuer", Generation: 1, NodeId: "node-a"}
	if _, err := api.BindAuthority(ctx, &command.AuthorityBindRequest{ExpectedBootId: caps.BootId, Authority: binding}); err != nil {
		t.Fatal(err)
	}
	challenge, err := api.GetAuthorityChallenge(ctx, &command.AuthorityChallengeRequest{ExpectedBootId: caps.BootId})
	if err != nil {
		t.Fatal(err)
	}
	share := &command.AuthorityShare{Rate: 8192, Burst: 64}
	g := &command.ExecutionGrant{Authority: binding, InstanceId: caps.InstanceId, BootId: caps.BootId, ClientId: "owner", WindowId: "window", PolicyVersion: 1, GrantId: "grant", Sequence: 1, ChallengeId: challenge.ChallengeId, Capacity: 960, Upload: share, Download: share, LeaseDurationMillis: 5000}
	if _, err := api.InstallAuthorityGrant(ctx, g); err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tcpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	exchange(t, tcp, bytes.Repeat([]byte{0x37}, 256))
	oldSeal, err := api.SealAuthorityGrant(ctx, &command.AuthorityGrantRequest{ExpectedBootId: caps.BootId, ClientId: g.ClientId, GrantId: g.GrantId, PreserveSessions: true})
	if err != nil || !oldSeal.GetSealed() || oldSeal.GetUsage().GetBilledBytes() != 768 {
		t.Fatalf("atomic TCP handoff boundary: %+v/%v", oldSeal, err)
	}
	changed := &clientpolicy.PolicyConfig{ClientId: g.ClientId, Version: 2, Enabled: true, MultiplierMicros: 2000000, UploadBytesPerSecond: 4096, DownloadBytesPerSecond: 4096, BurstBytes: 512}
	if _, err := api.ApplyPolicies(ctx, &command.ApplyRequest{Policies: []*clientpolicy.PolicyConfig{changed}}); err != nil {
		t.Fatal(err)
	}
	challenge, err = api.GetAuthorityChallenge(ctx, &command.AuthorityChallengeRequest{ExpectedBootId: caps.BootId})
	if err != nil {
		t.Fatal(err)
	}
	g = &command.ExecutionGrant{Authority: binding, InstanceId: caps.InstanceId, BootId: caps.BootId, ClientId: "owner", WindowId: "window-2", PolicyVersion: 2, GrantId: "grant-2", Sequence: 2, ChallengeId: challenge.ChallengeId, Capacity: 320, Upload: &command.AuthorityShare{Rate: 4096, Burst: 64}, Download: &command.AuthorityShare{Rate: 4096, Burst: 64}, LeaseDurationMillis: 5000}
	if _, err := api.InstallAuthorityGrant(ctx, g); err != nil {
		t.Fatal(err)
	}
	flow, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", udpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	packet := bytes.Repeat([]byte{0x84}, 64)
	if err := flow.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Write(packet); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 512)
	if n, err := flow.Read(reply); err != nil || !bytes.Equal(reply[:n], packet) {
		t.Fatalf("UDP packet changed: n=%d/%v", n, err)
	}
	exchange(t, tcp, bytes.Repeat([]byte{0x28}, 8))
	request := &command.AuthorityGrantRequest{ExpectedBootId: caps.BootId, ClientId: "owner", GrantId: g.GrantId}
	state, err := api.GetAuthorityGrant(ctx, request)
	if err != nil || state.GetUsage().GetRawUpload() != 72 || state.GetUsage().GetRawDownload() != 72 || state.GetUsage().GetBilledBytes() != 288 || state.GetUsage().GetRemainder() != 0 {
		t.Fatalf("shared Tunnel accounting: %+v/%v", state, err)
	}
	lifetime, err := api.GetClient(ctx, &command.ClientRequest{ClientId: g.ClientId})
	if err != nil || lifetime.GetUsage().GetRawUpload() != 328 || lifetime.GetUsage().GetRawDownload() != 328 || lifetime.GetUsage().GetBilledBytes() != 1056 {
		t.Fatalf("hot multiplier lifetime accounting: %+v/%v", lifetime, err)
	}
	// Exhaustion closes the owning connection at the admission boundary; the
	// final admitted buffer may be interrupted before reaching the endpoint.
	_, _ = tcp.Write([]byte("over-budget"))
	until := time.Now().Add(time.Second)
	for {
		state, err = api.GetAuthorityGrant(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if state.GetUsage().GetBilledBytes() == 320 {
			break
		}
		if !time.Now().Before(until) {
			t.Fatalf("grant exhaustion failed: %+v", state)
		}
		time.Sleep(time.Millisecond)
	}
	if state.GetUsage().GetRawUpload() != 83 || state.GetUsage().GetRawDownload() != 77 {
		t.Fatalf("finite grant admission overrun: %+v", state)
	}
	blocked, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tcpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	if err := blocked.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _ = blocked.Write([]byte("over-budget"))
	if _, err := blocked.Read(make([]byte, 1)); err == nil {
		t.Fatal("reconnection bypassed finite shared grant")
	}
	sealed, err := api.SealAuthorityGrant(ctx, request)
	if err != nil || !sealed.GetSealed() || sealed.GetUsage().GetBilledBytes() != 320 {
		t.Fatalf("real endpoint seal lost committed usage: %+v/%v", sealed, err)
	}
}
