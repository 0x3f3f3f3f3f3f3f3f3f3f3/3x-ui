package loopback

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
)

type chainDispatcher struct {
	routing.Dispatcher
	forward func(context.Context, *transport.Link) error
}

func (d *chainDispatcher) DispatchLink(ctx context.Context, _ net.Destination, link *transport.Link) error {
	return d.forward(ctx, link)
}

func TestLoopbackBoundsRoutingRecursion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hops  int
		cycle bool
		want  int
		err   string
	}{
		{"one-hop-cycle", 1, true, 1, "loopback routing cycle"},
		{"two-hop-cycle", 2, true, 2, "loopback routing cycle"},
		{"long-chain", 16, false, 16, ""},
		{"too-many-hops", 17, false, 16, "loopback hop limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loops := make([]*Loopback, tc.hops)
			calls := 0
			for i := range loops {
				loops[i] = &Loopback{inboundTag: fmt.Sprintf("hop-%d", i)}
			}
			for i, loop := range loops {
				loop.dispatcherInstance = &chainDispatcher{forward: func(ctx context.Context, link *transport.Link) error {
					calls++
					if calls > tc.hops {
						return errors.New("test recursion bound reached")
					}
					if i+1 == len(loops) && !tc.cycle {
						return nil
					}
					return loops[(i+1)%len(loops)].Process(ctx, link, nil)
				}}
			}
			ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: net.TCPDestination(net.LocalHostIP, 1234)}})
			err := loops[0].Process(ctx, &transport.Link{}, nil)
			if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("routing error: %v; want %q", err, tc.err)
			}
			if calls != tc.want {
				t.Fatalf("routing dispatches: %d; want %d", calls, tc.want)
			}
		})
	}
}
