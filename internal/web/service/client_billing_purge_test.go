package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"reflect"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestBilledDepletionPurgeKeepsDiscountedAndRenewingClients(t *testing.T) {
	for _, mode := range []string{"clients", "inbounds"} {
		t.Run(mode, func(t *testing.T) {
			testBilledDepletionPurge(t, mode)
		})
	}
}

func testBilledDepletionPurge(t *testing.T, mode string) {
	t.Helper()
	setupBulkDB(t)
	db := database.GetDB()
	for _, seed := range []struct {
		email               string
		raw, billed, mult   int64
		reset, day, weekday int
	}{
		{"discount", 120, 60, 500, 0, 0, 0},
		{"spent", 50, 100, 2000, 0, 0, 0},
		{"interval", 200, 200, 1000, 7, 0, 0},
		{"monthly", 200, 200, 1000, 0, 1, 0},
		{"weekly", 200, 200, 1000, 0, 0, 1},
	} {
		client := model.ClientRecord{Email: seed.email, Enable: true, TotalGB: 100, Reset: seed.reset, ResetDay: seed.day, ResetWeekday: seed.weekday}
		if err := db.Create(&client).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&xray.ClientTraffic{Email: seed.email, PolicyID: client.PolicyID, Up: seed.raw, Total: 100, Enable: true}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ClientUsageAccount{PolicyID: client.PolicyID, Up: seed.raw, Billed: seed.billed, Multiplier: seed.mult, Revision: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if mode == "clients" {
		if count, _, err := (&ClientService{}).DelDepleted(&InboundService{}); err != nil || count != 1 {
			t.Fatalf("purge %d, %v", count, err)
		}
	} else if err := (&InboundService{}).DelDepletedClients(-1); err != nil {
		t.Fatal(err)
	}
	var remaining []string
	if err := db.Model(&xray.ClientTraffic{}).Order("email").Pluck("email", &remaining).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(remaining, []string{"discount", "interval", "monthly", "weekly"}) {
		t.Fatalf("purge used raw instead of billed quota: %v", remaining)
	}
}

func TestBilledDepletionPurgeRespectsInboundScope(t *testing.T) {
	setupBulkDB(t)
	db := database.GetDB()
	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }})
	mgr.SetLocalRuntimeOverride(&fakeNodeRuntime{})
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(nil) })
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	credentials := &model.SSHClient{PublicKeys: []string{string(ssh.MarshalAuthorizedKey(key))}, Targets: []model.SSHTarget{{Host: "route.invalid", Port: 443}}}
	clients := []model.Client{
		{Email: "shared-spent", SubID: "shared-spent", Enable: true, TotalGB: 100, SSH: credentials},
		{Email: "shared-discount", SubID: "shared-discount", Enable: true, TotalGB: 100, SSH: credentials},
	}
	first := mkInbound(t, 49101, model.SSH, clientsSettings(t, clients))
	second := mkInbound(t, 49102, model.SSH, clientsSettings(t, clients))
	for i, seed := range []struct {
		raw, billed, mult int64
	}{{50, 100, 2000}, {120, 60, 500}} {
		rec := clients[i].ToRecord()
		if err := db.Create(rec).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&xray.ClientTraffic{InboundId: first.Id, Email: rec.Email, PolicyID: rec.PolicyID, Enable: true, Total: 100, Up: seed.raw}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ClientUsageAccount{PolicyID: rec.PolicyID, Multiplier: seed.mult, Billed: seed.billed, Up: seed.raw, Revision: 1}).Error; err != nil {
			t.Fatal(err)
		}
		for _, inbound := range []*model.Inbound{first, second} {
			if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: inbound.Id}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	inboundSvc := &InboundService{}
	if err := inboundSvc.DelDepletedClients(first.Id); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id   int
		want []string
	}{{first.Id, []string{"shared-discount"}}, {second.Id, []string{"shared-discount", "shared-spent"}}} {
		inbound, err := inboundSvc.GetInbound(test.id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := inboundSvc.GetClients(inbound)
		if err != nil || !reflect.DeepEqual(sortedEmails(got), test.want) {
			t.Fatalf("inbound %d membership = %v, %v", test.id, emailsOf(got), err)
		}
	}
	if got := trafficOf(t, "shared-spent"); got.Up != 50 {
		t.Fatalf("out-of-scope sibling lost shared traffic: %+v", got)
	}
	if err := inboundSvc.DelDepletedClients(-1); err != nil {
		t.Fatal(err)
	}
	var remaining []string
	if err := db.Model(&xray.ClientTraffic{}).Order("email").Pluck("email", &remaining).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(remaining, []string{"shared-discount"}) {
		t.Fatalf("global purge left incorrectly charged rows: %v", remaining)
	}
}

func TestBilledDepletionPurge_Postgres(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{{"clients", func(t *testing.T) { testBilledDepletionPurge(t, "clients") }}, {"inbounds", func(t *testing.T) { testBilledDepletionPurge(t, "inbounds") }}, {"scope", TestBilledDepletionPurgeRespectsInboundScope}} {
		t.Run(test.name, func(t *testing.T) {
			managedUsagePostgresSchema(t)
			test.run(t)
		})
	}
}
