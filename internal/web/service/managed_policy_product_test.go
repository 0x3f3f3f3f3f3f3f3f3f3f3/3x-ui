package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestManagedPolicyProductStatusRequiresExplicitOriginalActivation(t *testing.T) {
	setupPolicyLedgerDB(t)
	ctx := context.Background()
	t.Cleanup(func() { _ = StopManagedPolicyCoordinator(ctx) })
	t.Logf("managed policy product status backend: %s", database.GetDB().Dialector.Name())
	svc := &ManagedPolicyCoordinatorService{}
	status, err := svc.Status(ctx)
	if err != nil || status == nil || status.Active || status.AuthorityID != "" || status.Generation != "" {
		t.Fatalf("inactive status fabricated allocation owner: %+v/%v", status, err)
	}
	if _, err := os.Lstat(filepath.Join(config.GetDBFolderPath(), "client-policy", "coordinator")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reading inactive product status created original state")
	}
	active, err := svc.Activate(ctx)
	if err != nil || active == nil || !active.Active || active.AuthorityID == "" || active.Generation != "1" {
		t.Fatalf("explicit original activation unavailable: %+v/%v", active, err)
	}
	status, err = svc.Status(ctx)
	if err != nil || status == nil || *status != *active {
		t.Fatal("product status replaced original owner", err)
	}
	if _, err := svc.Status(nil); err == nil {
		t.Fatal("nil product context admitted owner")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.Activate(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled product activation changed owner", err)
	}
	if err := database.GetDB().Model(&model.ClientPolicyCoordinatorSource{}).Where("node_key = ?", managedCoordinatorSourceKey).Update("instance_id", "replacement-control-source").Error; err != nil {
		t.Fatal(err)
	}
	if status, err := svc.Status(ctx); err == nil || status != nil {
		t.Fatal("SQL replacement reported healthy original owner")
	}
}

func TestManagedPolicyProductAccountPagesPreserveOriginalFractionAndScope(t *testing.T) {
	setupPolicyLedgerDB(t)
	ctx, db := context.Background(), database.GetDB()
	t.Cleanup(func() { _ = StopManagedPolicyCoordinator(ctx) })
	svc := &ManagedPolicyCoordinatorService{}
	scope := model.ClientPolicyScopeGlobal
	parent := model.ClientRecord{Email: "product-global", Enable: true, TotalGB: 128, Policy: &model.ClientPolicyOptions{Scope: &scope, Multiplier: "1.5"}}
	nodeParent := model.ClientRecord{Email: "product-node", Enable: true, TotalGB: 128}
	for _, row := range []*model.ClientRecord{&parent, &nodeParent} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.Accounts(ctx, ManagedPolicyAccountPageRequest{ParentClientID: parent.StableID, Limit: 2})
	if err != nil || page == nil || !page.PendingEnrollment || len(page.Accounts) != 0 {
		t.Fatal("inactive status fabricated balances", err)
	}
	c, err := getManagedPolicyCoordinator(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	a := managedAuthorityMember{NodeID: "product-node-a", SourceID: "product-source-a"}
	b := managedAuthorityMember{NodeID: "product-node-b", SourceID: "product-source-b"}
	origin, _, err := c.PrepareAccount(ctx, parent.StableID, a)
	if err != nil {
		t.Fatal(err)
	}
	na, _, err := c.PrepareAccount(ctx, nodeParent.StableID, a)
	if err != nil {
		t.Fatal(err)
	}
	nb, _, err := c.PrepareAccount(ctx, nodeParent.StableID, b)
	if err != nil {
		t.Fatal(err)
	}
	j := c.state.Journal
	m := policyauthority.ClientMapping{Authority: j.Identity(), NodeAnchor: policyauthority.Identity{AuthorityID: "product-node-original", Generation: 1}, NodeID: a.NodeID, SourceID: a.SourceID, GlobalClientID: origin.ClientID, LocalClientID: "22222222-2222-4222-8222-222222222222", GlobalPolicyVersion: origin.InitialPolicyVersion, LocalPolicyVersion: 1, PolicyDigest: origin.PolicyDigest}
	if err := j.RecordClientMapping(policyauthority.ClientMappingCoordinator, m); err != nil {
		t.Fatal(err)
	}
	boot := policyauthority.NodeBoot{NodeID: a.NodeID, SourceID: a.SourceID, BootID: "product-boot"}
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	account, err := j.Account(origin.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := j.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: j.Identity(), NodeBoot: boot, ClientID: origin.ClientID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "product-display-grant", ChallengeID: "product-challenge", Capacity: 64, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Report(policyauthority.Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Usage: policyauthority.Usage{RawUpload: 1, BilledBytes: 1, Remainder: 500000}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ClientPolicyAuthorityProjection{}).Where("client_id = ?", origin.ClientID).Update("account_json", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	page, err = svc.Accounts(ctx, ManagedPolicyAccountPageRequest{ParentClientID: parent.StableID, Limit: 2})
	if err != nil || page == nil || len(page.Accounts) != 1 {
		t.Fatalf("original account unavailable: %+v/%v", page, err)
	}
	got := page.Accounts[0]
	if got.ClientID != origin.ClientID || got.Scope != "global" || got.PolicyVersion != strconv.FormatUint(origin.InitialPolicyVersion, 10) || got.Usage.Billed != "1.5" || got.Budget.Allocated != "62.5" || got.Budget.Unallocated == nil || *got.Budget.Unallocated != "64" || got.Remaining == nil || *got.Remaining != "126.5" {
		t.Fatalf("original fraction/budget lost: %+v", got)
	}
	contributions, err := svc.Contributions(ctx, ManagedPolicyContributionRequest{ParentClientID: parent.StableID, ClientID: origin.ClientID, Limit: 16})
	if err != nil || len(contributions.Contributions) != 1 {
		t.Fatal("original contribution unavailable", err)
	}
	contribution := contributions.Contributions[0]
	if contribution.NodeID != a.NodeID || contribution.SourceID != a.SourceID || contribution.BootID != boot.BootID || contribution.GrantID != grant.GrantID || contribution.GrantSequence != "1" || contribution.ReportSequence != "1" || contribution.Usage.Upload != "1" || contribution.Usage.Download != "0" || contribution.Usage.Billed != "1.5" {
		t.Fatalf("original contribution changed: %+v", contribution)
	}
	if _, err := svc.Contributions(ctx, ManagedPolicyContributionRequest{ParentClientID: nodeParent.StableID, ClientID: origin.ClientID, Limit: 16}); err == nil {
		t.Fatal("another parent disclosed original contributions")
	}
	page, err = svc.Accounts(ctx, ManagedPolicyAccountPageRequest{ParentClientID: nodeParent.StableID, Limit: 1})
	if err != nil || len(page.Accounts) != 1 || page.Accounts[0].ClientID != na.ClientID || page.NextNode != a.NodeID {
		t.Fatal("first independent node account lost", err)
	}
	page, err = svc.Accounts(ctx, ManagedPolicyAccountPageRequest{ParentClientID: nodeParent.StableID, AfterNode: page.NextNode, Limit: 1})
	if err != nil || len(page.Accounts) != 1 || page.Accounts[0].ClientID != nb.ClientID || page.Accounts[0].Remaining == nil || *page.Accounts[0].Remaining != "128" {
		t.Fatal("second node inherited global consumption", err)
	}
	if _, err := svc.Accounts(ctx, ManagedPolicyAccountPageRequest{ParentClientID: parent.StableID, Limit: 129}); err == nil {
		t.Fatal("unbounded product page admitted")
	}
	t.Logf("managed policy product account backend: %s", db.Dialector.Name())
}

func TestManagedPolicyProductEnrollmentRejectsInactiveAndMalformedRequests(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &ManagedPolicyCoordinatorService{}
	request := ManagedPolicyEnrollmentRequest{InventoryID: 1, ParentClientID: "11111111-1111-4111-8111-111111111111", NodeID: "node-a", SourceID: "source-a", LocalClientID: "22222222-2222-4222-8222-222222222222", LocalPolicyVersion: "1"}
	if request.Validate() != nil {
		t.Fatal("valid enrollment contract rejected")
	}
	for _, value := range []string{"", "01", "1.0", "1e0", "9223372036854775808"} {
		invalid := request
		invalid.LocalPolicyVersion = value
		if invalid.Validate() == nil {
			t.Fatal("noncanonical local version admitted", value)
		}
	}
	if result, err := svc.Enroll(context.Background(), request); err == nil || result != nil {
		t.Fatal("enrollment silently activated an owner")
	}
	if _, err := os.Lstat(filepath.Join(config.GetDBFolderPath(), "client-policy", "coordinator")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused enrollment created original state")
	}
	if result, err := svc.Enroll(nil, request); err == nil || result != nil {
		t.Fatal("nil enrollment context admitted")
	}
}

func TestManagedPolicyProductMaximumAccountPageContinuesWithinEnvelope(t *testing.T) {
	setupPolicyLedgerDB(t)
	ctx := context.Background()
	t.Cleanup(func() { _ = StopManagedPolicyCoordinator(ctx) })
	parent := model.ClientRecord{Email: "maximum-node-page", Enable: true, TotalGB: 128}
	if err := database.GetDB().Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	c, err := getManagedPolicyCoordinator(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 128; n++ {
		member := managedAuthorityMember{NodeID: fmt.Sprintf("maximum-node-%03d", n), SourceID: fmt.Sprintf("maximum-source-%03d", n)}
		if _, _, err := c.PrepareAccount(ctx, parent.StableID, member); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for n := 0; n < 129; n++ {
		page, err := (&ManagedPolicyCoordinatorService{}).Accounts(ctx, ManagedPolicyAccountPageRequest{ParentClientID: parent.StableID, AfterNode: cursor, Limit: 128})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(entity.Msg{Success: true, Obj: page})
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > panelruntime.NodeAuthorityMessageLimit {
			t.Fatalf("valid maximum page exceeded envelope: %d", len(raw))
		}
		for _, a := range page.Accounts {
			if seen[a.NodeID] || a.Usage.Upload != "0" || a.Usage.Download != "0" || a.Usage.Billed != "0" || a.Budget.Allocated != "0" {
				t.Fatalf("duplicated or fabricated balance: %+v", a)
			}
			seen[a.NodeID] = true
		}
		if page.NextNode == "" {
			break
		}
		if page.NextNode == cursor || len(page.Accounts) == 0 {
			t.Fatal("continuation made no progress")
		}
		cursor = page.NextNode
	}
	if len(seen) != 128 {
		t.Fatalf("continuation lost original accounts: %d", len(seen))
	}
	t.Logf("managed policy maximum page backend: %s", database.GetDB().Dialector.Name())
}
