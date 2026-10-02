package service

import (
	"context"
	"errors"
	"sync"
	"time"

	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var localAuthority struct {
	sync.RWMutex
	process   *panelxray.Process
	authority *managedAuthority
}

func retainManagedAuthority(process *panelxray.Process, authority *managedAuthority) error {
	localAuthority.Lock()
	defer localAuthority.Unlock()
	if localAuthority.authority != nil || process == nil || authority == nil {
		return ErrClientPolicyLedger
	}
	localAuthority.process, localAuthority.authority = process, authority
	return nil
}

func managedAuthorityForProcess(process *panelxray.Process) *managedAuthority {
	localAuthority.RLock()
	defer localAuthority.RUnlock()
	if localAuthority.process != process {
		return nil
	}
	return localAuthority.authority
}

func stopManagedAuthority(ctx context.Context, process *panelxray.Process) error {
	localAuthority.Lock()
	defer localAuthority.Unlock()
	if localAuthority.process != process || localAuthority.authority == nil {
		return nil
	}
	if err := localAuthority.authority.Stop(ctx); err != nil {
		return err
	}
	localAuthority.process, localAuthority.authority = nil, nil
	return nil
}

func stopManagedProcess(ctx context.Context, process *panelxray.Process) error {
	if ctx == nil || process == nil {
		return ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	authorityErr := stopManagedAuthority(ctx, process)
	processErr := process.Stop()
	var closeErr error
	if authorityErr != nil && !process.IsRunning() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		localAuthority.Lock()
		if localAuthority.process == process && localAuthority.authority != nil {
			var closed bool
			closed, closeErr = localAuthority.authority.closeStopped(closeCtx)
			if closed {
				localAuthority.process, localAuthority.authority = nil, nil
			}
		}
		localAuthority.Unlock()
	}
	return errors.Join(authorityErr, processErr, closeErr)
}
