package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/xtls/xray-core/app/commander"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var ErrInvalidAPIEndpoint = errors.New("invalid local Xray control endpoint")

func dialLocalControl(endpoint string) (*grpc.ClientConn, error) {
	return dialLocalControlChecked(endpoint, nil)
}

func dialLocalControlChecked(endpoint string, checkPeer func(net.Conn) error) (*grpc.ClientConn, error) {
	network := "tcp"
	if filepath.IsAbs(endpoint) {
		network = "unix"
		if err := commander.ValidatePrivateUnixSocket(endpoint); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidAPIEndpoint, err)
		}
		info, err := os.Lstat(endpoint)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidAPIEndpoint, err)
		}
		if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%w: socket must be private", ErrInvalidAPIEndpoint)
		}
	} else {
		host, port, err := net.SplitHostPort(endpoint)
		ip := net.ParseIP(host)
		n, parseErr := strconv.ParseUint(port, 10, 16)
		if err != nil || ip == nil || !ip.IsLoopback() || parseErr != nil || n == 0 {
			return nil, fmt.Errorf("%w: TCP requires a loopback IP and valid port", ErrInvalidAPIEndpoint)
		}
	}
	dialer := &net.Dialer{}
	return grpc.NewClient("passthrough:///"+endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			connection, err := dialer.DialContext(ctx, network, endpoint)
			if err != nil {
				return nil, err
			}
			if checkPeer != nil {
				if err := checkPeer(connection); err != nil {
					connection.Close()
					return nil, err
				}
			}
			return connection, nil
		}))
}

func (p *Process) GetAPIEndpoint() (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.config == nil {
		return "", fmt.Errorf("%w: no core configuration", ErrInvalidAPIEndpoint)
	}
	var api struct {
		Tag    string `json:"tag"`
		Listen string `json:"listen"`
	}
	if len(p.config.API) != 0 {
		if err := json.Unmarshal(p.config.API, &api); err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalidAPIEndpoint, err)
		}
	}
	if api.Listen != "" {
		return api.Listen, nil
	}
	if api.Tag == "" {
		api.Tag = "api"
	}
	for _, inbound := range p.config.InboundConfigs {
		if inbound.Tag == api.Tag {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(inbound.Port)), nil
		}
	}
	return "", fmt.Errorf("%w: no API listener", ErrInvalidAPIEndpoint)
}

func (x *XrayAPI) InitProcess(process *Process) error {
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		return err
	}
	return x.InitEndpoint(endpoint)
}
