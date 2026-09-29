package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyAccountingIncludesEverySnapshotBatch(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ClientPolicySource{}).Where("instance_id = ?", "core-a").Update("sequence", 1).Error; err != nil {
		t.Fatal(err)
	}
	clients := make([]model.ClientRecord, 1001)
	for i := range clients {
		clients[i] = model.ClientRecord{Email: fmt.Sprintf("accounting-batch-%04d", i), DesiredPolicyVersion: 1}
	}
	if err := db.CreateInBatches(&clients, 100).Error; err != nil {
		t.Fatal(err)
	}
	traffic := make([]xray.ClientTraffic, len(clients))
	receipts := make([]model.ClientPolicyReceipt, len(clients))
	totals := make([]model.ClientPolicyTotal, len(clients))
	for i, client := range clients {
		traffic[i] = xray.ClientTraffic{Email: client.Email}
		receipts[i] = model.ClientPolicyReceipt{ClientID: client.StableID, InstanceID: "core-a", Epoch: 1, Sequence: 1, PolicyVersion: 1, RawUpload: 3, BilledBytes: 1, Remainder: 500000}
		totals[i] = model.ClientPolicyTotal{ClientID: client.StableID, RawUpload: 3, BilledBytes: 1}
	}
	for _, rows := range []any{&traffic, &receipts, &totals} {
		if err := db.CreateInBatches(rows, 100).Error; err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	rows, err := (&InboundService{}).GetAllClientTraffics()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1001 {
		t.Fatalf("snapshot count = %d", len(rows))
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if got := row.Accounting; got == nil || got.Lifetime.Billed != "1.5" || got.Period.Upload != "3" || got.AppliedVersion != "1" {
			t.Fatalf("snapshot batch lost confirmed accounting: email=%s, accounting=%+v", row.Email, got)
		}
		seen[row.Accounting.ClientID] = true
	}
	if len(seen) != 1001 {
		t.Fatalf("snapshot lost stable identities: %d", len(seen))
	}
	t.Logf("1001-client accounting snapshot: %s", time.Since(started))
}

func TestClientPolicyAccountingFollowsRenameWithoutLeakingIntoReusedEmail(t *testing.T) {
	id := resetLedgerFixture(t)
	db := database.GetDB()
	var owner model.ClientRecord
	if err := db.First(&owner, "stable_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientGlobalTraffic{MasterGuid: "old-parent", Email: owner.Email, Up: 19, Down: 23}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeClientTraffic{NodeId: 7, Email: owner.Email, Up: 11, Down: 13}).Error; err != nil {
		t.Fatal(err)
	}
	cs, inbounds := &ClientService{}, &InboundService{}
	updated, err := cs.GetByID(owner.Id)
	if err != nil {
		t.Fatal(err)
	}
	updated.Email = "renamed-accounting"
	writeFailure := errors.New("injected traffic rename failure")
	if err := db.Callback().Update().Before("gorm:update").Register("test:accounting-rename", func(tx *gorm.DB) {
		if tx.Statement.Table == "client_traffics" {
			tx.AddError(writeFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, renameErr := cs.Update(inbounds, owner.Id, *updated.ToClient(), 0)
	if err := db.Callback().Update().Remove("test:accounting-rename"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(renameErr, writeFailure) {
		t.Fatalf("rename ignored traffic write failure: %v", renameErr)
	}
	var persisted model.ClientRecord
	if err := db.First(&persisted, owner.Id).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Email != "reset-fixture" {
		t.Fatalf("failed traffic rename changed stable record: %s", persisted.Email)
	}
	if _, err := cs.Update(inbounds, owner.Id, *updated.ToClient(), 0); err != nil {
		t.Fatal(err)
	}
	replacementID := policyLedgerClient(t, "reset-fixture", 0, 0)
	if replacementID == id {
		t.Fatal("replacement reused stable identity")
	}
	for _, email := range []string{"renamed-accounting", "reset-fixture"} {
		traffic, err := inbounds.GetClientTrafficByEmail(email)
		if err != nil || traffic == nil {
			t.Fatalf("read %s: %+v, %v", email, traffic, err)
		}
		if email == "reset-fixture" {
			if traffic.Accounting != nil || traffic.Up != 0 || traffic.Down != 0 {
				t.Fatalf("reused email inherited prior usage: %+v", traffic)
			}
		} else if got := traffic.Accounting; got == nil || got.ClientID != id || got.Lifetime.Billed != "1.5" {
			t.Fatalf("rename lost stable lifetime accounting: %+v", got)
		} else if traffic.Up != 19 || traffic.Down != 23 {
			t.Fatalf("rename lost the existing global display counters: %+v", traffic)
		}
	}
	var nodeUsage model.NodeClientTraffic
	if err := db.First(&nodeUsage, "node_id = ?", 7).Error; err != nil {
		t.Fatal(err)
	}
	if nodeUsage.Email != "renamed-accounting" || nodeUsage.Up != 11 || nodeUsage.Down != 13 {
		t.Fatalf("rename lost the existing node delta baseline: %+v", nodeUsage)
	}
}

func TestClientPolicyAccountingRejectsInconsistentCommittedState(t *testing.T) {
	for _, name := range []string{"missing-total", "total-mismatch", "multiple-sources", "remote-attachment", "future-reset"} {
		t.Run(name, func(t *testing.T) {
			id := resetLedgerFixture(t)
			db := database.GetDB()
			var err error
			switch name {
			case "missing-total":
				err = db.Delete(&model.ClientPolicyTotal{}, "client_id = ?", id).Error
			case "total-mismatch":
				err = db.Model(&model.ClientPolicyTotal{}).Where("client_id = ?", id).Update("billed_bytes", 2).Error
			case "multiple-sources":
				err = db.Create(&model.ClientPolicyReceipt{InstanceID: "other-core", ClientID: id}).Error
			case "remote-attachment":
				var owner model.ClientRecord
				if err := db.First(&owner, "stable_id = ?", id).Error; err != nil {
					t.Fatal(err)
				}
				remote := mkInbound(t, 24192, model.Tunnel, `{}`)
				if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
					t.Fatal(err)
				}
				err = db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: remote.Id}).Error
			case "future-reset":
				err = db.Create(&model.ClientPolicyReset{ClientID: id, RequestID: "invalid", InstanceID: "core-a", Epoch: 1, Sequence: 1, PolicyVersion: 2}).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (&InboundService{}).GetClientTrafficByEmail("reset-fixture"); !errors.Is(err, ErrClientPolicyLedger) {
				t.Fatalf("invalid committed state became a valid usage view: %v", err)
			}
		})
	}
}

func TestClientPolicyAccountingWaitsForResetAcknowledgement(t *testing.T) {
	id := resetLedgerFixture(t)
	read := func() *xray.ClientPolicyAccounting {
		t.Helper()
		traffic, err := (&InboundService{}).GetClientTrafficByEmail("reset-fixture")
		if err != nil {
			t.Fatal(err)
		}
		if traffic == nil || traffic.Accounting == nil {
			t.Fatalf("existing client statistics omitted committed accounting: %+v", traffic)
		}
		if traffic.Accounting.ClientID != id {
			t.Fatalf("statistics lost their stable identity: %+v", traffic.Accounting)
		}
		return traffic.Accounting
	}
	initial := read()
	if initial.Lifetime.Billed != "1.5" || initial.Lifetime.Uncertain != "7" || initial.Period.Upload != "3" || initial.Period.Billed != "1.5" || initial.ResetPending {
		t.Fatalf("initial projection lost confirmed billing or uncertainty: %+v", initial)
	}
	if _, err := PrepareClientPolicyReset("core-a", id, "first-window"); err != nil {
		t.Fatal(err)
	}
	settle := func(after, sequence, version, upload, billed, remainder uint64) *command.LedgerPage {
		t.Helper()
		page := policyLedgerPage(id, sequence, upload, 1, billed)
		page.Records[0].PolicyVersion = version
		page.Records[0].Usage.Remainder = remainder
		page.Records[0].UncertainBytes = 7
		if err := SettleClientPolicyLedger("core-a", 1, after, page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	settle(1, 2, 1, 5, 3, 0)
	pending := read()
	if pending.Period.Upload != "5" || pending.Period.Download != "1" || pending.Period.Billed != "3" || pending.Period.Uncertain != "7" || !pending.ResetPending || pending.AppliedVersion != "1" || pending.DesiredVersion != "2" {
		t.Fatalf("pending reset appeared applied before acknowledgement: %+v", pending)
	}
	settle(2, 3, 2, 5, 3, 0)
	first := read()
	if first.Period.Upload != "2" || first.Period.Download != "1" || first.Period.Billed != "1.5" || first.Period.Uncertain != "0" || first.Lifetime.Billed != "3" || first.Lifetime.Uncertain != "7" || first.ResetPending {
		t.Fatalf("acknowledged projection lost fraction borrow or lifetime uncertainty: %+v", first)
	}
	if _, err := PrepareClientPolicyReset("core-a", id, "second-window"); err != nil {
		t.Fatal(err)
	}
	settle(3, 4, 2, 6, 3, 500000)
	secondPending := read()
	if secondPending.Period.Upload != "3" || secondPending.Period.Download != "1" || secondPending.Period.Billed != "2" || !secondPending.ResetPending {
		t.Fatalf("new pending reset concealed the already applied window: %+v", secondPending)
	}
	page := settle(4, 5, 3, 6, 3, 500000)
	final := read()
	if final.Period.Upload != "1" || final.Period.Download != "0" || final.Period.Billed != "0.5" || final.Lifetime.Upload != "6" || final.Lifetime.Download != "1" || final.Lifetime.Billed != "3.5" || final.ResetPending || final.AppliedVersion != "3" {
		t.Fatalf("final projection confused period and lifetime usage: %+v", final)
	}
	if err := SettleClientPolicyLedger("core-a", 1, 4, page); err != nil {
		t.Fatal(err)
	}
	if retry := read(); !reflect.DeepEqual(retry, final) {
		t.Fatalf("receipt replay changed accounting: before %+v, after %+v", final, retry)
	}
}

func TestClientPolicyAccountingPreservesExactRemainingQuotaInJSON(t *testing.T) {
	id := resetLedgerFixture(t)
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", id).Update("total_gb", 10).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicies([]string{id}); err != nil {
		t.Fatal(err)
	}
	page := policyLedgerPage(id, 2, 3, 0, 1)
	page.Records[0].PolicyVersion = 2
	page.Records[0].Usage.Remainder = 500000
	page.Records[0].UncertainBytes = 7
	if err := SettleClientPolicyLedger("core-a", 1, 1, page); err != nil {
		t.Fatal(err)
	}
	traffic, err := (&InboundService{}).GetClientTrafficByEmail("reset-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if got := traffic.Accounting; got == nil || got.QuotaBytes != "10" || got.Remaining == nil || *got.Remaining != "1.5" {
		t.Fatalf("remaining quota omitted frozen uncertainty or its exact fraction: %+v", got)
	}
	if _, err := PrepareClientPolicyReset("core-a", id, "credit-prior-window"); err != nil {
		t.Fatal(err)
	}
	page.Records[0].Sequence, page.NextSequence = 3, 3
	page.Records[0].PolicyVersion = 3
	if err := SettleClientPolicyLedger("core-a", 1, 2, page); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", id).Update("total_gb", int64(math.MaxInt64)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicies([]string{id}); err != nil {
		t.Fatal(err)
	}
	page = policyLedgerPage(id, 4, math.MaxInt64-8, 0, math.MaxInt64-8)
	page.Records[0].PolicyVersion = 4
	page.Records[0].Usage.Remainder = 999999
	page.Records[0].UncertainBytes = 7
	if err := SettleClientPolicyLedger("core-a", 1, 3, page); err != nil {
		t.Fatal(err)
	}
	traffic, err = (&InboundService{}).GetClientTrafficByEmail("reset-fixture")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(traffic)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Accounting struct {
			QuotaBytes any `json:"quotaBytes"`
			Remaining  any `json:"remaining"`
			Lifetime   struct {
				Billed any `json:"billed"`
			} `json:"lifetime"`
			Period struct {
				Billed any `json:"billed"`
			} `json:"period"`
		} `json:"accounting"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Accounting
	if got.QuotaBytes != "9223372036854775807" || got.Lifetime.Billed != "9223372036854775799.999999" || got.Period.Billed != "9223372036854775798.499999" || got.Remaining != "8.500001" {
		t.Fatalf("JSON lost exact accounting bytes or fractional remainder: %+v", got)
	}
}

func TestClientPolicyAccountingReachesExistingListsAndSnapshots(t *testing.T) {
	id := resetLedgerFixture(t)
	var owner model.ClientRecord
	if err := database.GetDB().First(&owner, "stable_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&owner).Update("sub_id", "accounting-statistics").Error; err != nil {
		t.Fatal(err)
	}
	inbound := mkInbound(t, 24193, model.Tunnel, `{"address":"127.0.0.1","port":9001,"network":"tcp","clients":[]}`)
	clients, inbounds := &ClientService{}, &InboundService{}
	if _, err := clients.Attach(inbounds, owner.Id, []int{inbound.Id}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		read func() ([]*xray.ClientTraffic, error)
	}{
		{"full-snapshot", inbounds.GetAllClientTraffics},
		{"active-snapshot", func() ([]*xray.ClientTraffic, error) { return inbounds.GetActiveClientTraffics([]string{owner.Email}) }},
		{"client-list", func() ([]*xray.ClientTraffic, error) {
			rows, err := clients.List()
			if err != nil {
				return nil, err
			}
			var out []*xray.ClientTraffic
			for _, row := range rows {
				out = append(out, row.Traffic)
			}
			return out, nil
		}},
		{"paged-clients", func() ([]*xray.ClientTraffic, error) {
			page, err := clients.ListPaged(inbounds, nil, ClientPageParams{})
			if err != nil {
				return nil, err
			}
			var out []*xray.ClientTraffic
			for _, row := range page.Items {
				out = append(out, row.Traffic)
			}
			return out, nil
		}},
		{"inbound-list", func() ([]*xray.ClientTraffic, error) {
			rows, err := inbounds.GetInboundsSlim(inbound.UserId)
			if err != nil {
				return nil, err
			}
			var out []*xray.ClientTraffic
			for _, row := range rows {
				for i := range row.ClientStats {
					out = append(out, &row.ClientStats[i])
				}
			}
			return out, nil
		}},
		{"inbound-detail", func() ([]*xray.ClientTraffic, error) {
			row, err := inbounds.GetInboundDetail(inbound.Id)
			if err != nil {
				return nil, err
			}
			var out []*xray.ClientTraffic
			for i := range row.ClientStats {
				out = append(out, &row.ClientStats[i])
			}
			return out, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, err := test.read()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0] == nil || rows[0].Accounting == nil {
				t.Fatalf("read path discarded the accounting projection: %+v", rows)
			}
			if got := rows[0].Accounting; got.ClientID != id || got.Lifetime.Upload != "3" || got.Lifetime.Billed != "1.5" {
				t.Fatalf("read path replaced committed billing with legacy counters: %+v", got)
			}
		})
	}
}
