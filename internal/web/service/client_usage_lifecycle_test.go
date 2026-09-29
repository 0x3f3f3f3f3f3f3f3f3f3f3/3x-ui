package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func managedUsageFixture(t *testing.T, multiplier clientpolicy.Multiplier) (*database.ClientUsageLedger, model.ClientRecord, model.ClientUsageMeter, int) {
	t.Helper()
	setupBulkDB(t)
	return seedManagedUsageFixture(t, multiplier)
}

func seedManagedUsageFixture(t *testing.T, multiplier clientpolicy.Multiplier) (*database.ClientUsageLedger, model.ClientRecord, model.ClientUsageMeter, int) {
	t.Helper()
	client := model.Client{Email: "managed-usage", Password: "managed-usage-fixture", Enable: true, TotalGB: 1000}
	inbound := mkInbound(t, 31234, model.Mieru, clientsSettings(t, []model.Client{client}))
	db := database.GetDB()
	if err := db.Create(&xray.ClientTraffic{InboundId: inbound.Id, Email: client.Email, Enable: true, Total: 1000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	record := lookupClientRecord(t, client.Email)
	ledger := database.NewClientUsageLedger(db)
	if _, err := ledger.ChangeMultiplier(context.Background(), record.PolicyID, 1, multiplier, nil); err != nil {
		t.Fatal(err)
	}
	meter, err := ledger.ClaimAdmissionSource(context.Background(), record.PolicyID, "local/policy-controller")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(context.Background(), database.ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 150}); err != nil {
		t.Fatal(err)
	}
	return ledger, record, meter, inbound.Id
}

func TestManagedUsageTrafficTickPreservesIndependentRestrictionReasons(t *testing.T) {
	ledger, record, meter, _ := managedUsageFixture(t, 500)
	db := database.GetDB()
	for _, m := range []any{&model.ClientRecord{}, &xray.ClientTraffic{}} {
		column := "total"
		if _, ok := m.(*model.ClientRecord); ok {
			column = "total_gb"
		}
		if err := db.Model(m).Where("email = ?", record.Email).Update(column, 100).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &InboundService{}
	if _, disabled, err := svc.AddTraffic(nil, nil); err != nil || disabled {
		t.Fatalf("legacy tick disabled a 0.5x client at 75/100 billed bytes: disabled=%v err=%v", disabled, err)
	}
	if _, err := ledger.Admit(context.Background(), database.ClientUsageReport{MeterID: meter.ID, Sequence: 2, Up: 200}); err != nil {
		t.Fatalf("remaining billed credit was lost: %v", err)
	}
	if _, _, err := svc.AddTraffic(nil, nil); err != nil {
		t.Fatal(err)
	}
	if !lookupClientRecord(t, record.Email).Enable {
		t.Fatal("quota exhaustion was written as an operator disable")
	}
	if err := ledger.CheckAdmissionSource(context.Background(), meter.ID); !errors.Is(err, database.ErrUsageQuota) {
		t.Fatalf("quota reason was lost: %v", err)
	}
	past := time.Now().Add(-time.Second).UnixMilli()
	for _, m := range []any{&model.ClientRecord{}, &xray.ClientTraffic{}} {
		if err := db.Model(m).Where("email = ?", record.Email).Update("expiry_time", past).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := svc.AddTraffic(nil, nil); err != nil {
		t.Fatal(err)
	}
	if !lookupClientRecord(t, record.Email).Enable {
		t.Fatal("expiry was written as an operator disable")
	}
	if err := ledger.CheckAdmissionSource(context.Background(), meter.ID); !errors.Is(err, database.ErrUsageExpired) {
		t.Fatalf("expiry reason was lost: %v", err)
	}
}

func TestManagedUsagePanelResetsRetireMetersAndPreserveManualDisable(t *testing.T) {
	for _, kind := range []string{"client", "bulk", "all-clients", "inbound", "single-inbound", "detached", "all"} {
		t.Run(kind, func(t *testing.T) {
			ledger, record, meter, inboundID := managedUsageFixture(t, 1500)
			db := database.GetDB()
			for _, m := range []any{&model.ClientRecord{}, &xray.ClientTraffic{}} {
				if err := db.Model(m).Where("email = ?", record.Email).Update("enable", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			svc, clients := &InboundService{}, &ClientService{}
			var err error
			switch kind {
			case "client":
				_, err = clients.ResetTrafficByEmail(svc, record.Email)
			case "bulk":
				_, err = clients.BulkResetTraffic(svc, []string{record.Email})
			case "all-clients":
				err = clients.ResetAllClientTraffics(svc, -1)
			case "inbound":
				err = clients.ResetAllClientTraffics(svc, inboundID)
			case "single-inbound":
				_, err = svc.ResetClientTraffic(inboundID, record.Email)
			case "detached":
				err = svc.ResetClientTrafficByEmail(record.Email)
			case "all":
				_, err = clients.ResetAllTraffics()
			}
			if err != nil {
				t.Fatal(err)
			}
			account, err := ledger.Read(context.Background(), record.PolicyID)
			if err != nil || account.Up != 0 || account.Down != 0 || account.Billed != 0 || account.Remainder != 0 || account.Multiplier != 1500 || account.Revision != 3 {
				t.Fatalf("reset bypassed the durable account boundary: %+v, %v", account, err)
			}
			if lookupClientRecord(t, record.Email).Enable {
				t.Fatal("quota reset revived an operator-disabled client")
			}
			var projection xray.ClientTraffic
			if err := db.Where("email = ?", record.Email).First(&projection).Error; err != nil {
				t.Fatal(err)
			}
			if projection.Enable || projection.Up != 0 || projection.Down != 0 {
				t.Fatalf("reset corrupted raw projection or manual disable: %+v", projection)
			}
			if err := ledger.CheckAdmissionSource(context.Background(), meter.ID); !errors.Is(err, database.ErrUsageClosed) {
				t.Fatalf("old forwarding source survived reset: %v", err)
			}
		})
	}
}

func managedEchoFlow(t *testing.T, controller *policyflow.Controller, policyID string) net.Conn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	flow, err := controller.Open(context.Background(), policyID, server)
	if err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer flow.Close()
		_, _ = io.Copy(flow.Writer(policyflow.Download, server), server)
	}()
	t.Cleanup(func() { flow.Close(); <-done })
	return client
}

func managedEcho(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, "payload"); err != nil {
		t.Fatal(err)
	}
	var reply [7]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || string(reply[:]) != "payload" {
		t.Fatalf("live TCP flow failed: %q, %v", reply, err)
	}
}

func TestManagedUsageResetFencesLiveTCPOnceAcrossAttachments(t *testing.T) {
	ledger, record, _, _ := managedUsageFixture(t, 1500)
	db := database.GetDB()
	second := mkInbound(t, 31235, model.Mieru, clientsSettings(t, []model.Client{*record.ToClient()}))
	if err := (&ClientService{}).SyncInbound(nil, second.Id, []model.Client{*record.ToClient()}); err != nil {
		t.Fatal(err)
	}
	other := model.ClientRecord{Email: "unrelated-managed", Enable: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: other.Email, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	controller := policyflow.NewController(ledger, "local/policy-controller")
	t.Cleanup(controller.Close)
	for _, id := range []string{record.PolicyID, other.PolicyID} {
		if err := controller.Configure(context.Background(), id, policyflow.Rates{}); err != nil {
			t.Fatal(err)
		}
	}
	conn := managedEchoFlow(t, controller, record.PolicyID)
	unrelated := managedEchoFlow(t, controller, other.PolicyID)
	managedEcho(t, conn)
	managedEcho(t, unrelated)
	start := time.Now()
	if _, err := (&ClientService{}).ResetTrafficByEmail(&InboundService{}, record.Email); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(start.Add(1250 * time.Millisecond))
	var one [1]byte
	_, err := conn.Read(one[:])
	var netErr net.Error
	if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		t.Fatalf("old TCP flow survived reset past 1.25s: %v", err)
	}
	managedEcho(t, unrelated)
	account, err := ledger.Read(context.Background(), record.PolicyID)
	if err != nil || account.Revision != 3 || account.Up != 0 || account.Down != 0 || account.Billed != 0 || account.Remainder != 0 {
		t.Fatalf("one multi-attachment reset did not produce one atomic boundary: %+v, %v", account, err)
	}
	if err := controller.Configure(context.Background(), record.PolicyID, policyflow.Rates{}); err != nil {
		t.Fatal(err)
	}
	managedEcho(t, managedEchoFlow(t, controller, record.PolicyID))
	account, err = ledger.Read(context.Background(), record.PolicyID)
	if err != nil || account.Down != 7 || account.Billed != 10 || account.Remainder != 500 || account.Revision != 3 {
		t.Fatalf("fresh post-reset traffic was lost or used a stale multiplier: %+v, %v", account, err)
	}
	t.Logf("panel reset retired the existing TCP flow in %s; unrelated client remained usable", time.Since(start))
}

func TestManagedUsageResetRefusesUnsettledObservedSources(t *testing.T) {
	ledger, record, _, _ := managedUsageFixture(t, 1500)
	if _, err := ledger.Register(context.Background(), record.PolicyID, "observed-not-drained", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).BulkResetTraffic(&InboundService{}, []string{record.Email}); !errors.Is(err, database.ErrUsageBoundary) {
		t.Fatalf("panel reset silently abandoned uncommitted observed traffic: %v", err)
	}
	account, err := ledger.Read(context.Background(), record.PolicyID)
	if err != nil || account.Up != 150 || account.Billed != 225 || account.Revision != 2 {
		t.Fatalf("failed reset partly changed the account: %+v, %v", account, err)
	}
}

func TestManagedUsageBulkResetRollsBackAllClients(t *testing.T) {
	setupBulkDB(t)
	testManagedUsageBulkResetRollsBackAllClients(t)
}

func testManagedUsageBulkResetRollsBackAllClients(t *testing.T) {
	t.Helper()
	ledger, record, meter, _ := seedManagedUsageFixture(t, 1500)
	db := database.GetDB()
	other := model.ClientRecord{Email: "unsettled-second", Enable: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: other.Email, Enable: true, Up: 10}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Register(context.Background(), other.PolicyID, "observed/unsettled", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	group := model.ClientGroup{Name: "managed-reset", ResetUp: 100}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&record).Update("group_name", group.Name).Error; err != nil {
		t.Fatal(err)
	}
	affected, err := (&ClientService{}).BulkResetTraffic(&InboundService{}, []string{record.Email, other.Email})
	if !errors.Is(err, database.ErrUsageBoundary) || affected != 0 {
		t.Fatalf("bulk reset accepted an incomplete boundary: affected=%d, err=%v", affected, err)
	}
	account, err := ledger.Read(context.Background(), record.PolicyID)
	if err != nil || account.Up != 150 || account.Billed != 225 || account.Revision != 2 {
		t.Fatalf("later failure failed to roll back the first client's reset: %+v, %v", account, err)
	}
	if err := ledger.CheckAdmissionSource(context.Background(), meter.ID); err != nil {
		t.Fatalf("rolled-back reset still fenced the original source: %v", err)
	}
	var projection xray.ClientTraffic
	if err := db.Where("email = ?", record.Email).First(&projection).Error; err != nil || projection.Up != 150 {
		t.Fatalf("failed bulk reset lost the raw projection: %+v, %v", projection, err)
	}
	if err := db.First(&group, group.Id).Error; err != nil || group.ResetUp != 100 {
		t.Fatalf("failed bulk reset changed group history: %+v, %v", group, err)
	}
}

func TestManagedUsageResetKeepsExpiryRestriction(t *testing.T) {
	setupBulkDB(t)
	testManagedUsageResetKeepsExpiryRestriction(t)
}

func testManagedUsageResetKeepsExpiryRestriction(t *testing.T) {
	t.Helper()
	ledger, record, _, _ := seedManagedUsageFixture(t, 1500)
	db := database.GetDB()
	past := time.Now().Add(-time.Hour).UnixMilli()
	for _, m := range []any{&model.ClientRecord{}, &xray.ClientTraffic{}} {
		if err := db.Model(m).Where("email = ?", record.Email).Update("expiry_time", past).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&ClientService{}).ResetTrafficByEmail(&InboundService{}, record.Email); err != nil {
		t.Fatal(err)
	}
	fresh, err := ledger.ClaimAdmissionSource(context.Background(), record.PolicyID, "local/policy-controller")
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.CheckAdmissionSource(context.Background(), fresh.ID); !errors.Is(err, database.ErrUsageExpired) {
		t.Fatalf("quota reset removed the independent expiry restriction: %v", err)
	}
	account, err := ledger.Read(context.Background(), record.PolicyID)
	if err != nil || account.Up != 0 || account.Billed != 0 || account.Revision != 3 {
		t.Fatalf("expired client could not reset its accounting period: %+v, %v", account, err)
	}
}

func TestManagedUsageAutomaticRenewalKeepsManualDisable(t *testing.T) {
	ledger, record, _, inboundID := managedUsageFixture(t, 1500)
	db := database.GetDB()
	past := time.Now().Add(-time.Hour).UnixMilli()
	client := record.ToClient()
	client.Enable, client.ExpiryTime, client.Reset = false, past, 1
	if err := db.Model(&model.Inbound{}).Where("id = ?", inboundID).Update("settings", clientsSettings(t, []model.Client{*client})).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&ClientService{}).SyncInbound(nil, inboundID, []model.Client{*client}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Updates(map[string]any{"enable": false, "expiry_time": past, "reset": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&InboundService{}).AddTraffic(nil, nil); err != nil {
		t.Fatal(err)
	}
	account, err := ledger.Read(context.Background(), record.PolicyID)
	if err != nil || account.Up != 0 || account.Billed != 0 || account.Revision != 3 {
		t.Fatalf("automatic renewal bypassed the durable reset boundary: %+v, %v", account, err)
	}
	after := lookupClientRecord(t, record.Email)
	var traffic xray.ClientTraffic
	if err := db.Where("email = ?", record.Email).First(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	settings, _ := settingsClient(t, inboundID, record.Email)
	if after.Enable || traffic.Enable || settings.Enable || after.ExpiryTime <= time.Now().UnixMilli() || after.ExpiryTime != traffic.ExpiryTime || settings.ExpiryTime != after.ExpiryTime || traffic.ResetCount != 1 {
		t.Fatalf("renewal revived manual disable or left stale expiry: record=%+v traffic=%+v settings=%+v", after, traffic, settings)
	}
	fresh, err := ledger.ClaimAdmissionSource(context.Background(), record.PolicyID, "local/policy-controller")
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.CheckAdmissionSource(context.Background(), fresh.ID); !errors.Is(err, database.ErrUsageDisabled) {
		t.Fatalf("renewal removed the independent operator restriction: %v", err)
	}
}

func TestManagedUsageSingleResetReachesEachNodeOnce(t *testing.T) {
	ledger, record, _, firstID := managedUsageFixture(t, 1500)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/panel/api/clients/resetTraffic/managed-usage" {
			http.Error(w, "unexpected reset request", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)
	host, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	db := database.GetDB()
	node := model.Node{Name: "isolated-reset-node", Address: host, Port: port, Scheme: "http", BasePath: "/", ApiToken: "test-only", Enable: true, AllowPrivateAddress: true, TlsVerifyMode: "verify"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }}))
	t.Cleanup(func() { runtime.SetManager(nil) })
	for port := 31236; port <= 31237; port++ {
		inbound := mkInbound(t, port, model.VLESS, clientsSettings(t, []model.Client{*record.ToClient()}))
		if err := db.Model(inbound).Update("node_id", node.Id).Error; err != nil {
			t.Fatal(err)
		}
		// Model a pre-existing remote accounting snapshot; new unsupported attachments
		// are denied by the public service and are tested separately.
		if err := db.Create(&model.ClientInbound{ClientId: record.Id, InboundId: inbound.Id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&ClientService{}).ResetTrafficByEmail(&InboundService{}, record.Email); err != nil {
		t.Fatal(err)
	}
	account, err := ledger.Read(context.Background(), record.PolicyID)
	if calls.Load() != 1 || err != nil || account.Revision != 3 {
		t.Fatalf("one client reset was lost or multiplied by attachment count: node calls=%d, account=%+v, err=%v", calls.Load(), account, err)
	}
	var first model.Inbound
	if err := db.First(&first, firstID).Error; err != nil || first.LastTrafficResetTime == 0 {
		t.Fatalf("managed reset lost the inbound reset timestamp: %+v, %v", first, err)
	}
	if err := db.First(&node, node.Id).Error; err != nil || !node.ConfigDirty {
		t.Fatalf("managed reset lost the retry marker for its remote node: dirty=%v, err=%v", node.ConfigDirty, err)
	}
}

func TestManagedUsageQuotaIncreaseKeepsManualDisable(t *testing.T) {
	ledger, record, _, inboundID := managedUsageFixture(t, 1500)
	db := database.GetDB()
	client := record.ToClient()
	client.Enable, client.TotalGB = false, 100
	if err := db.Model(&model.Inbound{}).Where("id = ?", inboundID).Update("settings", clientsSettings(t, []model.Client{*client})).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&ClientService{}).SyncInbound(nil, inboundID, []model.Client{*client}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Updates(map[string]any{"enable": false, "total": 100}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&ClientService{}).BulkAdjust(&InboundService{}, []string{record.Email}, 0, 1000, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	after := lookupClientRecord(t, record.Email)
	if after.Enable || after.TotalGB != 1100 {
		t.Fatalf("increasing quota changed an independent manual disable: %+v", after)
	}
	account, err := ledger.Read(context.Background(), record.PolicyID)
	if err != nil || account.Billed != 225 || account.Up != 150 || account.Revision != 2 {
		t.Fatalf("quota edit rewrote historical usage: %+v, %v", account, err)
	}
}
