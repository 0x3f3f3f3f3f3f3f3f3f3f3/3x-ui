package distribution

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// The kernel releases ownership on normal exit or process death. Keeping the
// lock inode avoids stale-PID checks and unlink/recreate races during recovery.
func lockPromotion(installed string) (func(), error) {
	root, err := os.OpenRoot(filepath.Dir(installed))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(installed) + ".install.lock"
	existing, statErr := root.Lstat(name)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	if statErr == nil && !existing.Mode().IsRegular() {
		return nil, errors.New("installation lock is not a regular file")
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	current, statErr := root.Lstat(name)
	if err != nil || statErr != nil || !current.Mode().IsRegular() || !os.SameFile(current, opened) {
		_ = f.Close()
		return nil, errors.New("installation lock changed while opening")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another installation replacement owns the lock")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
