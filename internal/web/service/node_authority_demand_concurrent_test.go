package service

import (
	"context"
	"testing"
	"time"

	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestNodeAuthorityDemandWaitAllowsConcurrentDiscovery(t *testing.T) {
	f := newNodeControlFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.node.ReadAuthorityRequests(ctx, panelruntime.NodeAuthorityRequestsRequest{Binding: f.binding, Limit: 128})
		done <- err
	}()
	// The actual core long-polls an empty demand page until cancellation.
	time.Sleep(40 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("actual empty demand read ended before discovery: %v", err)
	default:
	}
	discovery, err := f.node.DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: f.binding.ExpectedInstanceID, ExpectedBootID: f.binding.ExpectedBootID})
	if err != nil || discovery == nil || *discovery.ExecutionRole != f.binding.Role() {
		t.Fatalf("actual demand wait blocked concurrent pinned discovery: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled actual long poll did not exit")
	}
}
