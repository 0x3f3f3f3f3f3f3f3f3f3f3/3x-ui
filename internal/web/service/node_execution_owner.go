package service

import (
	"path/filepath"

	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
)

// The caller owns the managed authority lock. Its open journal pins the role;
// neither SQL replacement nor a mutable configuration can select another issuer.
func (a *managedAuthority) validateExecutionRole() error {
	if a.state == nil || a.state.Journal == nil || a.state.Role.Validate() != nil {
		return ErrClientPolicyLedger
	}
	manifest, err := readAuthorityManifest(filepath.Join(filepath.Dir(a.config.StateFile), "authority"))
	if err != nil {
		return err
	}
	if manifest.Phase != "committed" || manifest.SourceID != a.state.SourceID || manifest.Identity != a.state.Journal.Identity() || manifest.role() != a.state.Role {
		return ErrClientPolicyLedger
	}
	return checkAuthorityExecutionRole(a.state.Journal, manifest)
}

func (a *managedAuthority) delegated() bool {
	return a.state != nil && a.state.Role.Mode == panelruntime.NodeExecutionDelegated
}

func (a *managedAuthority) delegatedBinding() *command.AuthorityBinding {
	r := a.state.Role
	return &command.AuthorityBinding{AuthorityId: r.AuthorityID, Generation: r.Generation, NodeId: r.NodeID}
}
