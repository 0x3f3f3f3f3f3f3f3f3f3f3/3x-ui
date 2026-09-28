package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestImportLegacySSHTrafficKeepsLedgerReady(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		t.Run(fmt.Sprintf("orphan=%t", orphan), func(t *testing.T) {
			portableTestSchema(t)
			record := clientPolicyFixture(t)
			svc, inboundSvc := &ClientService{}, &InboundService{}
			exported, err := svc.ExportAll()
			if err != nil || len(exported) != 1 {
				t.Fatalf("export=%+v err=%v", exported, err)
			}
			if _, err := svc.Delete(inboundSvc, record.Id, false); err != nil {
				t.Fatal(err)
			}
			item := exported[0]
			item.Policy = nil
			item.Client.TotalGB = 11
			item.Traffic = &ClientPortableTraffic{Up: 5, Down: 6}
			if orphan {
				item.InboundIds = nil
			}
			result, _, err := svc.ImportClients(inboundSvc, []ClientCreatePayload{item})
			if err != nil || result.Created != 1 || len(result.Skipped) != 0 {
				t.Fatalf("restore=%+v err=%v", result, err)
			}
			restored := lookupClientRecord(t, record.Email)
			if restored.PolicyID == record.PolicyID {
				t.Fatal("portable import reused a deleted accounting lifetime")
			}
			var account model.ClientUsageAccount
			if err := database.GetDB().Where("policy_id = ?", restored.PolicyID).First(&account).Error; err != nil {
				t.Fatalf("restored SSH client has no ledger: %v", err)
			}
			if account.Up != 5 || account.Down != 6 || account.Billed != 11 || account.Multiplier != 1000 || account.Remainder != 0 {
				t.Fatalf("legacy restore ledger=%+v, want raw 5/6 billed=11 at 1x", account)
			}
			ledger := database.NewClientUsageLedger(database.GetDB())
			meter, err := ledger.ClaimAdmissionSource(context.Background(), restored.PolicyID, "portable/test")
			if err != nil {
				t.Fatal(err)
			}
			err = ledger.CheckAdmissionSource(context.Background(), meter.ID)
			if !errors.Is(err, database.ErrUsageQuota) {
				t.Fatalf("exhausted imported client admission=%v, want quota denial", err)
			}
		})
	}
}

func TestImportClientRejectsInvalidTrafficBeforeCreation(t *testing.T) {
	for _, traffic := range []ClientPortableTraffic{{Up: -1}, {Down: -1}, {Up: math.MaxInt64, Down: 1}, {ResetCount: -1}} {
		t.Run(fmt.Sprintf("%+v", traffic), func(t *testing.T) {
			portableTestSchema(t)
			setupBulkDB(t)
			item := ClientCreatePayload{Client: model.Client{Email: "invalid@portable", Enable: true}, Traffic: &traffic}
			result, _, err := (&ClientService{}).ImportClients(&InboundService{}, []ClientCreatePayload{item})
			if err != nil || result.Created != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "invalid portable traffic") {
				t.Errorf("invalid traffic result=%+v err=%v", result, err)
			}
			assertPortableImportAbsent(t, item.Client.Email)
		})
	}
}

func TestImportClientRollsBackFailedRestore(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(fmt.Sprintf("attached=%t", attached), func(t *testing.T) {
			portableTestSchema(t)
			setupBulkDB(t)
			ib := mkInbound(t, 25101, model.VLESS, `{"clients":[]}`)
			installPortableFailure(t, "client_traffics", "UPDATE OF up", "", "injected portable failure")
			item := ClientCreatePayload{Client: model.Client{Email: "atomic@restore", Enable: true}, Traffic: &ClientPortableTraffic{Up: 5, Down: 6}}
			if attached {
				item.InboundIds = []int{ib.Id}
			}
			result, restart, err := (&ClientService{}).ImportClients(&InboundService{}, []ClientCreatePayload{item})
			if err != nil || result.Created != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "injected portable failure") || restart {
				t.Errorf("failed restore result=%+v restart=%t err=%v, want one skipped and no apply", result, restart, err)
			}
			assertPortableImportAbsent(t, item.Client.Email, ib.Id)
		})
	}
}

func TestImportClientRollsBackAllInboundBindings(t *testing.T) {
	portableTestSchema(t)
	setupBulkDB(t)
	first := mkInbound(t, 25102, model.VLESS, `{"clients":[]}`)
	second := mkInbound(t, 25103, model.VLESS, `{"clients":[]}`)
	installPortableFailure(t, "client_inbounds", "INSERT", fmt.Sprintf("NEW.inbound_id = %d", second.Id), "injected second binding failure")
	item := ClientCreatePayload{Client: model.Client{Email: "atomic@bindings", Enable: true}, InboundIds: []int{first.Id, second.Id}, Traffic: &ClientPortableTraffic{Up: 5}}
	result, restart, err := (&ClientService{}).ImportClients(&InboundService{}, []ClientCreatePayload{item})
	if err != nil || result.Created != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "injected second binding failure") || restart {
		t.Errorf("failed binding result=%+v restart=%t err=%v", result, restart, err)
	}
	assertPortableImportAbsent(t, item.Client.Email, first.Id, second.Id)
}

func TestImportClientNeverReusesExistingIdentity(t *testing.T) {
	portableTestSchema(t)
	setupBulkDB(t)
	svc := &ClientService{}
	ib := mkInbound(t, 25104, model.VLESS, `{"clients":[]}`)
	other := mkInbound(t, 25105, model.VLESS, `{"clients":[]}`)
	item := ClientCreatePayload{Client: model.Client{Email: "existing@portable", SubID: "same-sub", Enable: false, TotalGB: 17, Comment: "original"}, InboundIds: []int{ib.Id}}
	if _, err := svc.Create(&InboundService{}, &item); err != nil {
		t.Fatal(err)
	}
	before := lookupClientRecord(t, item.Client.Email)
	item.Client.Enable = true
	item.Client.TotalGB = 0
	item.Client.Comment = "overwritten"
	item.InboundIds = []int{other.Id}
	result, _, err := svc.ImportClients(&InboundService{}, []ClientCreatePayload{item})
	if err != nil || result.Created != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "email already in use") {
		t.Errorf("duplicate result=%+v err=%v, want skipped existing identity", result, err)
	}
	after := lookupClientRecord(t, item.Client.Email)
	if before != after {
		t.Errorf("import changed existing client: before=%+v after=%+v", before, after)
	}
	var links int64
	if err := database.GetDB().Model(&model.ClientInbound{}).Where("client_id = ?", before.Id).Count(&links).Error; err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Errorf("existing client has %d bindings, want original one", links)
	}
}

type portableAdmissionRuntime struct {
	fakeNodeRuntime
	observed chan xray.ClientTraffic
}

func (r *portableAdmissionRuntime) AddClient(_ context.Context, _ *model.Inbound, client model.Client) error {
	var traffic xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", client.Email).First(&traffic).Error; err != nil {
		return err
	}
	r.observed <- traffic
	return nil
}

func TestImportClientRestoresTrafficBeforeRuntimeAdmission(t *testing.T) {
	portableTestSchema(t)
	setupBulkDB(t)
	nodeID, _ := setupNodeRuntime(t)
	probe := &portableAdmissionRuntime{observed: make(chan xray.ClientTraffic, 1)}
	runtime.GetManager().SetRuntimeOverride(nodeID, probe)
	ib := nodeInbound(t, nodeID, 25106, nil)
	item := ClientCreatePayload{Client: model.Client{Email: "admission@portable", Enable: true}, InboundIds: []int{ib.Id}, Traffic: &ClientPortableTraffic{Up: 17, Down: 23, ResetCount: 2}}
	result, _, err := (&ClientService{}).ImportClients(&InboundService{}, []ClientCreatePayload{item})
	if err != nil || result.Created != 1 || len(result.Skipped) != 0 {
		t.Fatalf("import result=%+v err=%v", result, err)
	}
	select {
	case traffic := <-probe.observed:
		if traffic.Up != 17 || traffic.Down != 23 || traffic.ResetCount != 2 {
			t.Fatalf("runtime admitted with traffic=%+v, want restored 17/23 and resetCount=2", traffic)
		}
	default:
		t.Fatal("runtime admission was not exercised")
	}
}

func assertPortableImportAbsent(t *testing.T, email string, inboundIDs ...int) {
	t.Helper()
	db := database.GetDB()
	for _, table := range []string{"clients", "client_traffics"} {
		var count int64
		if err := db.Table(table).Where("email = ?", email).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("failed import left %d rows in %s", count, table)
		}
	}
	for _, id := range inboundIDs {
		var ib model.Inbound
		if err := db.First(&ib, id).Error; err != nil {
			t.Fatal(err)
		}
		if strings.Contains(ib.Settings, email) {
			t.Errorf("failed import left client on inbound %d", id)
		}
		var count int64
		if err := db.Model(&model.ClientInbound{}).Where("inbound_id = ?", id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("failed import left %d links on inbound %d", count, id)
		}
	}
}

func portableTestSchema(t *testing.T) {
	t.Helper()
	if os.Getenv("XUI_DB_TYPE") == "postgres" {
		managedUsagePostgresSchema(t)
	}
}

func TestPortableRestoration_Postgres(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"rollback-traffic", TestImportClientRollsBackFailedRestore},
		{"rollback-bindings", TestImportClientRollsBackAllInboundBindings},
		{"duplicate", TestImportClientNeverReusesExistingIdentity},
		{"before-runtime", TestImportClientRestoresTrafficBeforeRuntimeAdmission},
		{"legacy-ssh", TestImportLegacySSHTrafficKeepsLedgerReady},
		{"invalid-traffic", TestImportClientRejectsInvalidTrafficBeforeCreation},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("XUI_DB_TYPE", "postgres")
			test.run(t)
		})
	}
}

func installPortableFailure(t *testing.T, table, operation, condition, reason string) {
	t.Helper()
	db := database.GetDB()
	if condition != "" {
		condition = " WHEN (" + condition + ")"
	}
	statement := "CREATE TRIGGER fail_portable BEFORE " + operation + " ON " + table
	if db.Name() == "postgres" {
		if err := db.Exec("CREATE FUNCTION fail_portable_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION '" + reason + "'; END $$").Error; err != nil {
			t.Fatal(err)
		}
		statement += " FOR EACH ROW" + condition + " EXECUTE FUNCTION fail_portable_write()"
	} else {
		statement += condition + " BEGIN SELECT RAISE(ABORT, '" + reason + "'); END"
	}
	if err := db.Exec(statement).Error; err != nil {
		t.Fatal(err)
	}
}
