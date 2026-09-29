package service

import (
	"errors"
	"math"
	"os"
	"sync"
	"testing"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func setupPolicyLedgerDB(t *testing.T) {
	t.Helper()
	cleanup, err := testpg.IsolatePackage("policy_" + t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	setupBulkDB(t)
}

func policyLedgerClient(t *testing.T, email string, up, down int64) string {
	t.Helper()
	c := model.ClientRecord{Email: email}
	if err := database.GetDB().Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: email, Up: up, Down: down}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicyLedger("core-a", c.StableID); err != nil {
		t.Fatal(err)
	}
	return c.StableID
}

func policyLedgerPage(id string, seq, up, down, bill uint64) *command.LedgerPage {
	return &command.LedgerPage{NextSequence: seq, Records: []*command.LedgerRecord{{InstanceId: "core-a", Epoch: 1, Sequence: seq, ClientId: id, PolicyVersion: 1, Usage: &command.Usage{RawUpload: up, RawDownload: down, BilledBytes: bill}}}}
}

func policyLedgerTotal(t *testing.T, id string) model.ClientPolicyTotal {
	t.Helper()
	var total model.ClientPolicyTotal
	if err := database.GetDB().Where("client_id = ?", id).First(&total).Error; err != nil {
		t.Fatal(err)
	}
	return total
}

func TestClientPolicyLedgerLegacyAndReplay(t *testing.T) {
	setupPolicyLedgerDB(t)
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	id := policyLedgerClient(t, "legacy", 111, 222)
	seed, err := PrepareClientPolicyLedger("core-a", id)
	if err != nil || seed.RawUpload != 111 || seed.RawDownload != 222 || seed.BilledBytes != 333 {
		t.Fatalf("seed = %v, %v", seed, err)
	}
	first := policyLedgerPage(id, 4, 121, 242, 393)
	first.Records[0].Usage.Remainder = 500000
	first.Records[0].UncertainBytes, first.Records[0].ReservedBytes = 20, 40
	if err := SettleClientPolicyLedger("core-a", 1, 0, first); err != nil {
		t.Fatal(err)
	}
	second := policyLedgerPage(id, 9, 122, 243, 394)
	second.Records[0].PolicyVersion = 2
	second.Records[0].UncertainBytes = 20
	if err := SettleClientPolicyLedger("core-a", 2, 4, second); err != nil {
		t.Fatal(err)
	}
	for _, page := range []*command.LedgerPage{first, second, first} {
		if err := SettleClientPolicyLedger("core-a", 2, 0, page); err != nil {
			t.Fatal(err)
		}
	}
	got := policyLedgerTotal(t, id)
	if got.RawUpload != 122 || got.RawDownload != 243 || got.BilledBytes != 394 || got.UncertainBytes != 20 {
		t.Fatalf("totals = %+v", got)
	}
	seed, err = PrepareClientPolicyLedger("core-a", id)
	if err != nil || seed.BilledBytes != 333 {
		t.Fatalf("retry changed original seed: %v/%v", seed, err)
	}
	var old xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", "legacy").First(&old).Error; err != nil {
		t.Fatal(err)
	}
	if old.Up != 111 || old.Down != 222 {
		t.Fatal("pre-activation ledger changed legacy stats")
	}
	if cursor, err := ClientPolicyLedgerCursor("core-a"); err != nil || cursor != 9 {
		t.Fatalf("cursor=%d/%v", cursor, err)
	}
}

func TestClientPolicyLedgerRejectsInvalidPagesAtomically(t *testing.T) {
	setupPolicyLedgerDB(t)
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	a, b := policyLedgerClient(t, "a", 0, 0), policyLedgerClient(t, "b", 0, 0)
	first := policyLedgerPage(a, 3, 10, 20, 60)
	if err := SettleClientPolicyLedger("core-a", 1, 0, first); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*command.LedgerPage){
		"raw regression":    func(p *command.LedgerPage) { p.Records[1].Usage.RawUpload = 9 },
		"billed regression": func(p *command.LedgerPage) { p.Records[1].Usage.BilledBytes = 59 },
		"overflow":          func(p *command.LedgerPage) { p.Records[1].Usage.BilledBytes = math.MaxUint64 },
		"bad remainder":     func(p *command.LedgerPage) { p.Records[1].Usage.Remainder = 1000000 },
		"wrong instance":    func(p *command.LedgerPage) { p.Records[1].InstanceId = "other" },
		"future epoch":      func(p *command.LedgerPage) { p.Records[1].Epoch = 2 },
		"unknown client":    func(p *command.LedgerPage) { p.Records[1].ClientId = "missing" },
		"unsorted":          func(p *command.LedgerPage) { p.Records[1].Sequence = 4 },
		"wrong next":        func(p *command.LedgerPage) { p.NextSequence = 99 },
		"nil usage":         func(p *command.LedgerPage) { p.Records[1].Usage = nil },
	} {
		t.Run(name, func(t *testing.T) {
			page := policyLedgerPage(b, 4, 1, 2, 3)
			page.Records = append(page.Records, policyLedgerPage(a, 5, 11, 21, 62).Records[0])
			page.NextSequence = 5
			mutate(page)
			if err := SettleClientPolicyLedger("core-a", 1, 3, page); err == nil {
				t.Fatal("invalid page accepted")
			}
			if got := policyLedgerTotal(t, b); got.BilledBytes != 0 {
				t.Fatal("partial page committed")
			}
			if got := policyLedgerTotal(t, a); got.BilledBytes != 60 {
				t.Fatal("earlier total changed")
			}
			if cursor, _ := ClientPolicyLedgerCursor("core-a"); cursor != 3 {
				t.Fatal("cursor advanced on failure")
			}
		})
	}
	if err := SettleClientPolicyLedger("core-a", 1, 4, policyLedgerPage(a, 5, 11, 21, 62)); err == nil {
		t.Fatal("cursor gap accepted")
	}
	if err := BindClientPolicySource("local", "replacement", 1); err == nil {
		t.Fatal("source silently replaced")
	}
	if err := BindClientPolicySource("other", "core-a", 1); err == nil {
		t.Fatal("one core bound to two nodes")
	}
}

func TestClientPolicyLedgerWriteFailureAndConcurrentRetry(t *testing.T) {
	setupPolicyLedgerDB(t)
	StartTrafficWriter()
	t.Cleanup(StopTrafficWriter)
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	id := policyLedgerClient(t, "a", 0, 0)
	page := policyLedgerPage(id, 1, 10, 20, 60)
	db := database.GetDB()
	injected := errors.New("injected ledger write failure")
	if err := db.Callback().Update().Before("gorm:update").Register("test:ledger-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_totals" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := SettleClientPolicyLedger("core-a", 1, 0, page)
	if removeErr := db.Callback().Update().Remove("test:ledger-failure"); removeErr != nil {
		t.Fatal(removeErr)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("expected injected failure: %v", err)
	}
	if got := policyLedgerTotal(t, id); got.BilledBytes != 0 {
		t.Fatal("failed write changed total")
	}
	if cursor, _ := ClientPolicyLedgerCursor("core-a"); cursor != 0 {
		t.Fatal("failed write advanced cursor")
	}
	var receipt model.ClientPolicyReceipt
	if err := db.Where("client_id = ?", id).First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.Sequence != 0 {
		t.Fatal("failed write advanced receipt")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := SettleClientPolicyLedger("core-a", 1, 0, proto.Clone(page).(*command.LedgerPage)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := policyLedgerTotal(t, id); got.BilledBytes != 60 {
		t.Fatalf("retry double charged: %+v", got)
	}
}

func TestClientPolicyLedgerDeletedIdentityKeepsItsReceipts(t *testing.T) {
	setupPolicyLedgerDB(t)
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	id := policyLedgerClient(t, "same-email", 0, 0)
	if err := database.GetDB().Where("stable_id = ?", id).Delete(&model.ClientRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	replacement := model.ClientRecord{Email: "same-email"}
	if err := database.GetDB().Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	page := policyLedgerPage(id, 1, 10, 20, 30)
	page.Records[0].Revoked = true
	if err := SettleClientPolicyLedger("core-a", 1, 0, page); err != nil {
		t.Fatal(err)
	}
	if got := policyLedgerTotal(t, id); got.BilledBytes != 30 {
		t.Fatal("deleted client's final receipt lost")
	}
	if replacement.StableID == id {
		t.Fatal("new client inherited deleted identity")
	}
	if _, err := PrepareClientPolicyLedger("core-a", id); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted identity can still be prepared for activation: %v", err)
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientPolicyTotal{}).Where("client_id = ?", replacement.StableID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("old receipt attributed to new client")
	}
}

func TestClientPolicyLedgerPostgresCommitFailure(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires a dedicated PostgreSQL test database")
	}
	setupPolicyLedgerDB(t)
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	id := policyLedgerClient(t, "commit-failure", 0, 0)
	db := database.GetDB()
	installDeferredCommitFailure(t, db, "update", "test:policy-commit", "client_policy_sources", "policy_fail_parent", "policy_fail_child")
	page := policyLedgerPage(id, 1, 10, 20, 60)
	if err := SettleClientPolicyLedger("core-a", 1, 0, page); err == nil {
		t.Fatal("deferred constraint did not fail at commit")
	}
	if got := policyLedgerTotal(t, id); got.BilledBytes != 0 || got.RawUpload != 0 || got.RawDownload != 0 {
		t.Fatal("commit failure retained totals")
	}
	if cursor, _ := ClientPolicyLedgerCursor("core-a"); cursor != 0 {
		t.Fatal("commit failure retained cursor")
	}
	var receipt model.ClientPolicyReceipt
	if err := db.First(&receipt, "client_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.Sequence != 0 || receipt.BilledBytes != 0 {
		t.Fatal("commit failure retained receipt")
	}
	if err := db.Callback().Update().Remove("test:policy-commit"); err != nil {
		t.Fatal(err)
	}
	if err := SettleClientPolicyLedger("core-a", 1, 0, page); err != nil {
		t.Fatal(err)
	}
	if got := policyLedgerTotal(t, id); got.BilledBytes != 60 {
		t.Fatal("retry lost committed usage")
	}
}
