package xray

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"sync"

	"github.com/xtls/xray-core/app/trafficcontrol"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

var ErrTrafficDrainCapability = errors.New("boot-scoped final traffic control is unavailable")

type trafficControlClient struct {
	connection *grpc.ClientConn
	client     trafficcontrol.TrafficControlServiceClient
	boot       string
	supported  bool
}

func dialTrafficControl(ctx context.Context, endpoint, expectedBoot string, expectedPID int) (*trafficControlClient, error) {
	if !filepath.IsAbs(endpoint) {
		return nil, ErrTrafficDrainCapability
	}
	var peerMu sync.Mutex
	var peerError error
	connection, err := dialLocalControlChecked(endpoint, func(connection net.Conn) error {
		err := verifyTrafficControlPeer(connection, expectedPID)
		if err != nil {
			peerMu.Lock()
			peerError = err
			peerMu.Unlock()
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	client := trafficcontrol.NewTrafficControlServiceClient(connection)
	capabilities, err := client.GetCapabilities(ctx, &trafficcontrol.Empty{})
	if err != nil {
		connection.Close()
		peerMu.Lock()
		defer peerMu.Unlock()
		if peerError != nil {
			return nil, fmt.Errorf("%w: %w", ErrTrafficDrainCapability, peerError)
		}
		return nil, err
	}
	if capabilities.GetApiVersion() != 1 || capabilities.GetBootId() == "" || expectedBoot != "" && capabilities.GetBootId() != expectedBoot {
		connection.Close()
		return nil, fmt.Errorf("%w: version or boot identity mismatch", ErrTrafficDrainCapability)
	}
	return &trafficControlClient{connection: connection, client: client, boot: capabilities.BootId, supported: slices.Contains(capabilities.Capabilities, "boot-scoped-final-counters-v1")}, nil
}

func (c *trafficControlClient) finalCounters(ctx context.Context) (map[string]int64, error) {
	if !c.supported {
		return nil, ErrTrafficDrainCapability
	}
	counters := make(map[string]int64)
	after := ""
	for {
		page, err := c.client.Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: c.boot, AfterName: after, Limit: 1000})
		if err != nil {
			return nil, err
		}
		if page.GetBootId() != c.boot || len(page.GetCounters()) > 1000 || proto.Size(page) > 1<<20 {
			return nil, fmt.Errorf("%w: invalid final counter page", ErrTrafficDrainCapability)
		}
		last := ""
		for name, value := range page.Counters {
			if name <= after || value < 0 {
				return nil, fmt.Errorf("%w: invalid final counter", ErrTrafficDrainCapability)
			}
			if _, exists := counters[name]; exists {
				return nil, fmt.Errorf("%w: duplicate final counter", ErrTrafficDrainCapability)
			}
			counters[name] = value
			if name > last {
				last = name
			}
		}
		if page.NextName == "" {
			return counters, nil
		}
		if last == "" || page.NextName != last {
			return nil, fmt.Errorf("%w: invalid final counter cursor", ErrTrafficDrainCapability)
		}
		after = page.NextName
	}
}
