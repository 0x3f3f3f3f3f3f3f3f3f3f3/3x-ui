package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/distribution"
)

func (s *XrayService) PublishRuntimeHealth(stopping bool) error {
	process := currentXrayProcess()
	pid := 0
	ready := false
	if process != nil {
		pid = process.PID()
		ready = process.IsControlReady() && s.GetHeldBackConfig() == ""
	}
	return distribution.PublishRuntimeHealth(config.GetDBFolderPath(), pid, ready && !stopping)
}
