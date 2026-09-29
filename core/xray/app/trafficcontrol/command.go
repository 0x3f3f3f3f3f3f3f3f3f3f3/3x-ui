package trafficcontrol

import (
	"context"
	"errors"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type service struct {
	UnimplementedTrafficControlServiceServer
	core *core.Instance
}

func (*service) RequiresPrivateUnixSocket() bool { return true }
func (s *service) Register(server *grpc.Server)  { RegisterTrafficControlServiceServer(server, s) }

func authorize(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil || p.Addr.Network() != "unix" {
		return status.Error(codes.PermissionDenied, "traffic control requires a private Unix socket")
	}
	return nil
}

func (s *service) GetCapabilities(ctx context.Context, _ *Empty) (*Capabilities, error) {
	if err := authorize(ctx); err != nil {
		return nil, err
	}
	response := &Capabilities{ApiVersion: 1, CoreVersion: core.VersionStatement()[0], BootId: s.core.TrafficDrainBootID()}
	if s.core.CanDrainTraffic() {
		response.Capabilities = []string{"boot-scoped-final-counters-v1"}
	}
	return response, nil
}

func (s *service) Drain(ctx context.Context, request *DrainRequest) (*DrainResponse, error) {
	if err := authorize(ctx); err != nil {
		return nil, err
	}
	if request.GetExpectedBootId() == "" {
		return nil, status.Error(codes.InvalidArgument, "expected boot identity is required")
	}
	counters, next, err := s.core.DrainTrafficPage(ctx, request.GetExpectedBootId(), request.GetAfterName(), request.GetLimit())
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, status.FromContextError(err).Err()
		case errors.Is(err, core.ErrTrafficDrainBoot):
			return nil, status.Error(codes.Aborted, err.Error())
		case errors.Is(err, core.ErrTrafficDrainUnsupported):
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		case errors.Is(err, core.ErrTrafficDrainPage):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		case errors.Is(err, core.ErrTrafficDrainCounterSize):
			return nil, status.Error(codes.ResourceExhausted, err.Error())
		default:
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return &DrainResponse{BootId: s.core.TrafficDrainBootID(), Counters: counters, NextName: next}, nil
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, _ interface{}) (interface{}, error) {
		return &service{core: core.MustFromContext(ctx)}, nil
	}))
}
