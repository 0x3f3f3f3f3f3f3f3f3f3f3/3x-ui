package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ManagedPolicyBootstrap struct {
	AfterSequence   uint64
	Initializations []*command.InitializeRequest
}

type ManagedProcessRuntime interface {
	StartManagedProcess(context.Context, *xray.Process, func(context.Context, *command.Capabilities) (*ManagedPolicyBootstrap, error)) error
}

func (l *Local) StartManagedProcess(ctx context.Context, process *xray.Process, prepare func(context.Context, *command.Capabilities) (*ManagedPolicyBootstrap, error)) error {
	if prepare == nil {
		return errors.New("managed policy preparation is required")
	}
	return process.StartManaged(ctx, func(ctx context.Context, api *xray.ClientPolicyAPI) error {
		bootstrap, err := prepare(ctx, api.Capabilities())
		if err != nil {
			return err
		}
		if bootstrap == nil || len(bootstrap.Initializations) > 100000 {
			return errors.New("invalid managed policy bootstrap")
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		if _, err := api.ReadLedger(ctx, bootstrap.AfterSequence, 1); err != nil {
			return fmt.Errorf("core is behind the panel ledger: %w", err)
		}
		for _, request := range bootstrap.Initializations {
			if request == nil || request.Policy == nil || request.Usage == nil {
				return errors.New("managed policy initialization requires policy and usage")
			}
			current, err := api.GetClient(ctx, request.Policy.ClientId)
			if status.Code(err) == codes.NotFound {
				if err := api.Initialize(ctx, request.Policy, request.Usage); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if current.Usage == nil || current.Usage.RawUpload < request.Usage.RawUpload || current.Usage.RawDownload < request.Usage.RawDownload || current.Usage.BilledBytes < request.Usage.BilledBytes {
				return errors.New("core usage is behind the historical initialization seed")
			}
		}
		return nil
	})
}
