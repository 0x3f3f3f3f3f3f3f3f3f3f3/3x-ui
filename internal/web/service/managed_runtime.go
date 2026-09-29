package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/sshoutbound"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type managedRuntimePlan struct {
	ssh       *sshRuntimePlan
	mieru     *mieruRuntimePlan
	outbounds []sshoutbound.Outbound
}

func (plan *managedRuntimePlan) hasServices() bool {
	return len(plan.ssh.entries) > 0 || len(plan.mieru.entries) > 0
}

func (plan *managedRuntimePlan) apply(cfg *xray.Config) {
	plan.ssh.apply(cfg)
	plan.mieru.apply(cfg)
}

func stageManagedRuntime(cfg *xray.Config) func() {
	ssh, mieru := stageSSHRuntime(cfg), stageMieruRuntime(cfg)
	return func() { mieru(); ssh() }
}
