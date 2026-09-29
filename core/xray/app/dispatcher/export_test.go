package dispatcher

import (
	"context"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport"
)

func ManageLinkForTest(manager clientpolicy.Manager, ctx context.Context, target net.Destination, link *transport.Link) (context.Context, func(), error) {
	d := &DefaultDispatcher{clients: manager}
	return d.manageLink(ctx, target, link)
}
