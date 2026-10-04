package policy_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
)

// applyFixturePolicyRPC performs the real private-API sealed handoff for an
// isolated finite-grant fixture. It is not a durable coordinator or allocator.
func applyFixturePolicyRPC(t *testing.T, ctx context.Context, api command.ClientPolicyServiceClient, engine *clientpolicy.Engine, policy *clientpolicy.PolicyConfig) {
	t.Helper()
	previous, err := engine.GetAuthorityGrant(policy.ClientId)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := api.SealAuthorityGrant(ctx, &command.AuthorityGrantRequest{ExpectedBootId: previous.Grant.BootID, ClientId: policy.ClientId, GrantId: previous.Grant.GrantID, PreserveSessions: true})
	if err != nil || !sealed.GetSealed() {
		t.Fatal("private fixture original seal failed", err)
	}
	if _, err := api.ApplyPolicies(ctx, &command.ApplyRequest{Policies: []*clientpolicy.PolicyConfig{policy}}); err != nil {
		t.Fatal(err)
	}
	challenge, err := api.GetAuthorityChallenge(ctx, &command.AuthorityChallengeRequest{ExpectedBootId: previous.Grant.BootID})
	if err != nil {
		t.Fatal(err)
	}
	grant := proto.Clone(sealed.Grant).(*command.ExecutionGrant)
	grant.Sequence++
	grant.GrantId = fmt.Sprintf("%s:%d", policy.ClientId, grant.Sequence)
	grant.PolicyVersion, grant.ChallengeId = policy.Version, challenge.ChallengeId
	grant.Upload, grant.Download = &command.AuthorityShare{Unlimited: true}, &command.AuthorityShare{Unlimited: true}
	if policy.UploadBytesPerSecond != 0 {
		grant.Upload = &command.AuthorityShare{Rate: policy.UploadBytesPerSecond, Burst: policy.BurstBytes}
	}
	if policy.DownloadBytesPerSecond != 0 {
		grant.Download = &command.AuthorityShare{Rate: policy.DownloadBytesPerSecond, Burst: policy.BurstBytes}
	}
	if _, err := api.InstallAuthorityGrant(ctx, grant); err != nil {
		t.Fatal("private fixture replacement failed", err)
	}
}
