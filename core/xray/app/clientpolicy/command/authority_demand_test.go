package command

import (
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPrivateAuthorityDemandRPCRequiresPeerAndCurrentBinding(t *testing.T) {
	s, ctx := authorityService(t)
	caps, err := s.GetCapabilities(ctx, &Empty{})
	if err != nil {
		t.Fatal(err)
	}
	binding := &AuthorityBinding{AuthorityId: "demand-issuer", Generation: 1, NodeId: "demand-node"}
	request := &AuthorityBindRequest{ExpectedBootId: caps.BootId, Authority: binding}
	if _, err := s.EnableAuthorityRequests(context.Background(), request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("nonprivate demand enabled: %v", err)
	}
	if _, err := s.ReadAuthorityRequests(context.Background(), &AuthorityRequestsRequest{ExpectedBootId: caps.BootId, Authority: binding, Limit: 128}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("nonprivate demand read: %v", err)
	}
	if _, err := s.BindAuthority(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableAuthorityRequests(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadAuthorityRequests(ctx, &AuthorityRequestsRequest{ExpectedBootId: "copied-boot", Authority: binding, Limit: 128}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("copied boot read demand: %v", err)
	}
	if _, err := s.ReadAuthorityRequests(ctx, &AuthorityRequestsRequest{ExpectedBootId: caps.BootId, Authority: binding, Limit: 129}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unbounded demand read: %v", err)
	}
}

func TestPrivateAuthorityDemandRPCPreservesRequestAndNoTrafficAllowance(t *testing.T) {
	s, ctx := authorityService(t)
	policy := clientpolicy.Policy{ClientID: "private-demand", Version: 1, Enabled: true, Multiplier: 1500000, BurstBytes: 64}
	if err := s.engine.Apply(policy); err != nil {
		t.Fatal(err)
	}
	caps, err := s.GetCapabilities(ctx, &Empty{})
	if err != nil {
		t.Fatal(err)
	}
	binding := &AuthorityBinding{AuthorityId: "demand-issuer", Generation: 1, NodeId: "demand-node"}
	bind := &AuthorityBindRequest{ExpectedBootId: caps.BootId, Authority: binding}
	if _, err := s.BindAuthority(ctx, bind); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableAuthorityRequests(ctx, bind); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan error, 1)
	go func() {
		session, err := s.engine.Open(wait, clientpolicy.Metadata{ClientID: policy.ClientID}, nil)
		if session != nil {
			session.Close()
		}
		opened <- err
	}()
	read, readCancel := context.WithTimeout(ctx, time.Second)
	defer readCancel()
	page, err := s.ReadAuthorityRequests(read, &AuthorityRequestsRequest{ExpectedBootId: caps.BootId, Authority: binding, Limit: 128})
	if err != nil || page.GetInstanceId() != caps.InstanceId || page.GetBootId() != caps.BootId || len(page.GetRequests()) != 1 || page.Requests[0].ClientId != policy.ClientID || len(page.Requests[0].RequestId) != 32 || page.Requests[0].PolicyVersion != 1 {
		t.Fatalf("private demand reply changed binding: %+v/%v", page, err)
	}
	cancel()
	if err := <-opened; err != context.Canceled {
		t.Fatalf("private read granted business admission: %v", err)
	}
	snapshot, err := s.engine.Snapshot(policy.ClientID)
	if err != nil || snapshot.Usage != (clientpolicy.Usage{}) || snapshot.ActiveSessions != 0 {
		t.Fatalf("private demand read billed: %+v/%v", snapshot, err)
	}
}
