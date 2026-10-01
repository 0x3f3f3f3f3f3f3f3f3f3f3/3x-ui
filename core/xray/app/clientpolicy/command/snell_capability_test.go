package command

import (
	"context"
	"net"
	"slices"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/grpc/peer"
)

func TestCapabilitiesAdvertiseVerifiedNativeSnell(t *testing.T) {
	engine := clientpolicy.NewEngine()
	t.Cleanup(func() { _ = engine.Close() })
	service := &service{engine: engine}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.UnixAddr{Net: "unix", Name: "test-private"}})
	caps, err := service.GetCapabilities(ctx, &Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(caps.Capabilities, "trusted-snell-client-id-v1") {
		t.Fatal("verified native Snell support not advertised")
	}
}
