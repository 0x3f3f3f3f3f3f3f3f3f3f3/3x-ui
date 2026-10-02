package command

import (
	"context"
	"net"
	"slices"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestUsageReconciliationRPCRequiresPrivateCurrentBootBeforeAuthorityBinding(t *testing.T) {
	s, ctx := authorityService(t)
	caps, err := s.GetCapabilities(ctx, &Empty{})
	if err != nil || !slices.Contains(caps.Capabilities, "dormant-monotone-usage-reconciliation-v1") {
		t.Fatalf("missing live recovery capability: %+v/%v", caps, err)
	}
	p := &clientpolicy.PolicyConfig{ClientId: "restored", Version: 1, Enabled: true, MultiplierMicros: 1000000, BurstBytes: 65536}
	if _, err := s.InitializeClient(ctx, &InitializeRequest{Policy: p, Usage: &Usage{BilledBytes: 10}}); err != nil {
		t.Fatal(err)
	}
	request := &ReconcileUsageRequest{ExpectedBootId: caps.BootId, ClientId: p.ClientId, Usage: &Usage{RawUpload: 5, BilledBytes: 15, Remainder: 500000}}
	for _, nonprivate := range []context.Context{context.Background(), peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}})} {
		if _, err := s.ReconcileUsage(nonprivate, request); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("nonprivate recovery edit: %v", err)
		}
	}
	wrong := proto.Clone(request).(*ReconcileUsageRequest)
	wrong.ExpectedBootId = "retired-boot"
	if _, err := s.ReconcileUsage(ctx, wrong); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("old boot recovery edit: %v", err)
	}
	if _, err := s.ReconcileUsage(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileUsage(ctx, request); err != nil {
		t.Fatalf("lost-reply retry: %v", err)
	}
	state, err := s.GetClient(ctx, &ClientRequest{ClientId: p.ClientId})
	if err != nil || state.Usage.BilledBytes != 15 || state.Usage.Remainder != 500000 || state.Usage.RawUpload != 5 {
		t.Fatalf("RPC changed known usage: %+v/%v", state, err)
	}
	if _, err := s.BindAuthority(ctx, &AuthorityBindRequest{ExpectedBootId: caps.BootId, Authority: &AuthorityBinding{AuthorityId: "issuer", Generation: 1, NodeId: "local"}}); err != nil {
		t.Fatal(err)
	}
	request.Usage.BilledBytes = 30
	if _, err := s.ReconcileUsage(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("bound authority accepted usage editing: %v", err)
	}
}
