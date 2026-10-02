package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// Caller owns restart serialization and has confirmed this core has exited.
// Retain the stale socket as evidence, and refuse any changed or unowned path.
func (a *managedAuthority) preserveStoppedSocket(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.socketPath == "" {
		return nil
	}
	current, err := os.Lstat(a.socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if a.socketInfo == nil || a.socketBoot == "" || current.Mode()&os.ModeSocket == 0 || !os.SameFile(current, a.socketInfo) {
		return ErrClientPolicyLedger
	}
	archive := a.socketPath + ".retired-" + a.socketBoot
	if _, err := os.Lstat(archive); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(ErrClientPolicyLedger, err)
	}
	if err := os.Rename(a.socketPath, archive); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(archive))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
