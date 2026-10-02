package service

import "fmt"

// Restore may proceed only after the tracked core's wait goroutine confirms
// absence. An already absent core is different from a failed termination.
func (s *ServerService) stopCoreForDatabaseRestore() error {
	lock.Lock()
	defer lock.Unlock()
	isManuallyStopped.Store(true)
	process := currentXrayProcess()
	if process == nil || !process.IsRunning() {
		return nil
	}
	err := process.Stop()
	if !process.IsRunning() {
		return nil
	}
	if err != nil {
		return fmt.Errorf("core remains running after stop: %w", err)
	}
	return fmt.Errorf("core remains running after stop returned success")
}
