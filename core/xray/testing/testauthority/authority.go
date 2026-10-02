// Package testauthority supplies explicit finite grants to isolated native
// endpoint fixtures. It does not implement an issuer journal or prove aggregate
// allocation across nodes; those require the control-plane integration gates.
package testauthority

import (
	"fmt"
	"math"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
)

func Grant(t testing.TB, engine *clientpolicy.Engine, policy clientpolicy.Policy) {
	t.Helper()
	caps := engine.Capabilities()
	binding := clientpolicy.AuthorityBinding{AuthorityID: "native-test-authority", Generation: 1, NodeID: "native-test-node"}
	if err := engine.BindAuthority(caps.BootID, binding); err != nil {
		t.Fatal(err)
	}
	sequence := uint64(1)
	if previous, err := engine.GetAuthorityGrant(policy.ClientID); err == nil {
		if !previous.Sealed {
			t.Fatal("fixture replacement requires a sealed accounting boundary")
		}
		sequence = previous.Grant.Sequence + 1
	}
	challenge, err := engine.BeginAuthorityChallenge(caps.BootID)
	if err != nil {
		t.Fatal(err)
	}
	upload, download := clientpolicy.AuthorityShare{Unlimited: true}, clientpolicy.AuthorityShare{Unlimited: true}
	if policy.UploadRate != 0 {
		upload = clientpolicy.AuthorityShare{Rate: policy.UploadRate, Burst: policy.BurstBytes}
	}
	if policy.DownloadRate != 0 {
		download = clientpolicy.AuthorityShare{Rate: policy.DownloadRate, Burst: policy.BurstBytes}
	}
	capacity := uint64(math.MaxInt64)
	if policy.QuotaBytes != 0 && policy.QuotaBytes < capacity {
		capacity = policy.QuotaBytes
	}
	grant := clientpolicy.ExecutionGrant{Authority: binding, InstanceID: caps.InstanceID, BootID: caps.BootID, ClientID: policy.ClientID, WindowID: "native-test-window", PolicyVersion: policy.Version, GrantID: fmt.Sprintf("%s:%d", policy.ClientID, sequence), Sequence: sequence, ChallengeID: challenge.ChallengeID, Capacity: capacity, Upload: upload, Download: download, LeaseDuration: clientpolicy.MaxAuthorityLeaseDuration}
	if _, err := engine.InstallAuthorityGrant(grant); err != nil {
		t.Fatal(err)
	}
}

// Apply simulates the issuer's sealed handoff around one native fixture policy
// change. The actual process RPC path is tested independently in policy tests.
func Apply(t testing.TB, engine *clientpolicy.Engine, policy clientpolicy.Policy) error {
	t.Helper()
	if previous, err := engine.GetAuthorityGrant(policy.ClientID); err == nil && !previous.Sealed {
		if _, err := engine.PauseAuthorityGrant(policy.ClientID, previous.Grant.GrantID); err != nil {
			return err
		}
	}
	if err := engine.Apply(policy); err != nil {
		return err
	}
	Grant(t, engine, policy)
	return nil
}
