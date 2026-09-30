package service

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func tunnelOwnerRequest(t *testing.T, ownerID string, fields map[string]any) *model.Inbound {
	t.Helper()
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["ownerClientId"] = ownerID
	if _, ok := fields["protocol"]; !ok {
		fields["protocol"] = "tunnel"
	}
	if _, ok := fields["settings"]; !ok {
		fields["settings"] = json.RawMessage(`{"address":"127.0.0.1","port":9001,"network":"tcp,udp","clients":[]}`)
	}
	fields["port"] = 24101
	fields["listen"] = "127.0.0.1"
	fields["streamSettings"] = json.RawMessage(`{}`)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var inbound model.Inbound
	if err := json.Unmarshal(raw, &inbound); err != nil {
		t.Fatal(err)
	}
	return &inbound
}

func TestTunnelOwnerSelectionReadsAuthoritativeIdentity(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owner := &model.ClientRecord{Email: "renamed-owner", Enable: true}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	inbound := mkInbound(t, 24105, model.Tunnel, `{"clients":[{"email":"stale-email"}]}`)
	if err := db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &InboundService{}
	for _, method := range []string{"detail", "full", "slim"} {
		var rows []*model.Inbound
		var err error
		switch method {
		case "detail":
			row, readErr := svc.GetInboundDetail(inbound.Id)
			rows, err = []*model.Inbound{row}, readErr
		case "full":
			rows, err = svc.GetInbounds(inbound.UserId)
		case "slim":
			rows, err = svc.GetInboundsSlim(inbound.UserId)
		}
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: rows=%d err=%v", method, len(rows), err)
		}
		raw, err := json.Marshal(rows[0])
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		if response["ownerClientId"] != owner.StableID {
			t.Errorf("%s returned wrong owner: %v", method, response["ownerClientId"])
		}
	}
	page, err := (&ClientService{}).ListPaged(svc, &SettingService{}, ClientPageParams{Page: 1, PageSize: 25, Search: owner.Email})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("owner choices: %v %v", page, err)
	}
	raw, err := json.Marshal(page.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	var choice map[string]any
	if err := json.Unmarshal(raw, &choice); err != nil {
		t.Fatal(err)
	}
	if choice["clientId"] != owner.StableID {
		t.Fatal("paged owner choice omitted the stable identity")
	}
}

func TestTunnelOwnerSelectionRollsBackLateLinkFailure(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owner := &model.ClientRecord{Email: "atomic-owner", Enable: true}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected ownership failure")
	const callback = "test:tunnel-owner-link-failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_inbounds" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	if _, _, err := (&InboundService{}).AddInbound(tunnelOwnerRequest(t, owner.StableID, nil)); !errors.Is(err, injected) {
		t.Fatalf("late ownership failure was swallowed: %v", err)
	}
	for _, table := range []string{"inbounds", "client_traffics", "client_inbounds"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s survived rollback: %d %v", table, count, err)
		}
	}
}

func TestTunnelOwnerSelectionUpdateRollsBackHistoryAndMembership(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owners := []model.ClientRecord{{Email: "rollback-old-owner"}, {Email: "rollback-new-owner"}}
	if err := db.Create(&owners).Error; err != nil {
		t.Fatal(err)
	}
	svc := &InboundService{}
	inbound, _, err := svc.AddInbound(tunnelOwnerRequest(t, owners[0].StableID, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", owners[0].Email).Updates(map[string]any{"up": 123, "down": 456}).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected ownership replacement failure")
	const callback = "test:tunnel-owner-update-rollback"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_inbounds" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	if _, _, err := svc.UpdateInbound(tunnelOwnerRequest(t, owners[1].StableID, map[string]any{"id": inbound.Id})); !errors.Is(err, injected) {
		t.Fatalf("late update error was swallowed: %v", err)
	}
	stored, err := svc.GetInboundDetail(inbound.Id)
	if err != nil || stored.Settings != inbound.Settings || stored.OwnerClientID == nil || *stored.OwnerClientID != owners[0].StableID {
		t.Fatalf("failed replacement changed owner: %+v %v", stored, err)
	}
	var traffic []xray.ClientTraffic
	if err := db.Find(&traffic).Error; err != nil || len(traffic) != 1 || traffic[0].Email != owners[0].Email || traffic[0].InboundId != inbound.Id || traffic[0].Up != 123 || traffic[0].Down != 456 {
		t.Fatalf("failed replacement changed history: %+v %v", traffic, err)
	}
}

func TestTunnelOwnerSelectionReadRejectsAmbiguityAndDatabaseFailure(t *testing.T) {
	for _, scenario := range []string{"ambiguous", "database-error"} {
		t.Run(scenario, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			inbound := mkInbound(t, 24112, model.Tunnel, `{"clients":[]}`)
			if scenario == "ambiguous" {
				owners := []model.ClientRecord{{Email: "ambiguous-a"}, {Email: "ambiguous-b"}}
				if err := db.Create(&owners).Error; err != nil {
					t.Fatal(err)
				}
				for _, owner := range owners {
					if err := db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}).Error; err != nil {
						t.Fatal(err)
					}
				}
			} else if err := db.Migrator().DropTable(&model.ClientInbound{}); err != nil {
				t.Fatal(err)
			}
			if _, err := (&InboundService{}).GetInboundDetail(inbound.Id); err == nil {
				t.Fatal("failed owner lookup was presented as an unowned listener")
			}
			if _, err := (&InboundService{}).GetInbounds(inbound.UserId); err == nil {
				t.Fatal("full list hid invalid owner lookup")
			}
			if _, err := (&InboundService{}).GetInboundsSlim(inbound.UserId); err == nil {
				t.Fatal("slim list hid invalid owner lookup")
			}
		})
	}
}

func TestTunnelOwnerSelectionRejectsLaterRemoteAttachment(t *testing.T) {
	for _, operation := range []string{"sync", "attach", "legacy-tunnel"} {
		t.Run(operation, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			owner := &model.ClientRecord{Email: "unprepared-owner", SubID: "unprepared-owner", UUID: "0663c9ca-5385-4691-9c09-6baeb2d728f3", Enable: true}
			if err := db.Create(owner).Error; err != nil {
				t.Fatal(err)
			}
			remote := mkInbound(t, 24106, model.VLESS, `{"clients":[]}`)
			if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
				t.Fatal(err)
			}
			svc, clients := &InboundService{}, &ClientService{}
			if operation == "legacy-tunnel" {
				if err := db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: remote.Id}).Error; err != nil {
					t.Fatal(err)
				}
				_, _, err := svc.AddInbound(&model.Inbound{Protocol: model.Tunnel, Port: 24101, Settings: clientsSettings(t, []model.Client{*owner.ToClient()})})
				if !errors.Is(err, ErrClientPolicyLedger) {
					t.Fatalf("legacy Tunnel linked a remote owner: %v", err)
				}
				return
			}
			if _, _, err := svc.AddInbound(tunnelOwnerRequest(t, owner.StableID, nil)); err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == "sync" {
				err = clients.SyncInbound(nil, remote.Id, []model.Client{*owner.ToClient()})
			} else {
				_, err = clients.Attach(svc, owner.Id, []int{remote.Id})
			}
			if !errors.Is(err, ErrClientPolicyLedger) {
				t.Fatalf("remote operation attached an unprepared Tunnel owner: %v", err)
			}
			if len(linksOf(t, remote.Id)) != 0 {
				t.Fatal("rejected remote attachment persisted a link")
			}
		})
	}
}

func TestTunnelOwnerSelectionRejectsLegacyRemoteGraphBeforeUpdateFanout(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owner := &model.ClientRecord{Email: "historical-owner", SubID: "historical-owner", UUID: "0663c9ca-5385-4691-9c09-6baeb2d728f3", Comment: "preserved", Enable: true}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	var before []string
	var inbounds []*model.Inbound
	for i, protocol := range []model.Protocol{model.VLESS, model.Tunnel, model.VLESS} {
		inbound := mkInbound(t, 24113+i, protocol, clientsSettings(t, []model.Client{*owner.ToClient()}))
		if err := db.Model(inbound).Update("enable", false).Error; err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			if err := db.Model(inbound).Update("node_id", 7).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}).Error; err != nil {
			t.Fatal(err)
		}
		inbounds, before = append(inbounds, inbound), append(before, inbound.Settings)
	}
	changed := owner.ToClient()
	changed.Comment = "must not partially save"
	if _, err := (&ClientService{}).Update(&InboundService{}, owner.Id, *changed, 0); !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("historical unsupported scope: %v", err)
	}
	if got := lookupClientRecord(t, owner.Email); got.Comment != "preserved" {
		t.Fatal("unsupported graph update changed the account before rejecting a later listener")
	}
	for i, inbound := range inbounds {
		if err := db.First(inbound, inbound.Id).Error; err != nil || inbound.Settings != before[i] {
			t.Fatalf("unsupported graph update changed listener %d: %v", i, err)
		}
	}
}

func TestTunnelOwnerSelectionRehomesCanonicalHistoryWithStaleSettings(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owners := []model.ClientRecord{{Email: "actual-former-owner"}, {Email: "replacement-owner"}}
	if err := db.Create(&owners).Error; err != nil {
		t.Fatal(err)
	}
	inbound := mkInbound(t, 24101, model.Tunnel, `{"clients":[{"email":"stale-former-name"}]}`)
	if err := db.Create(&model.ClientInbound{ClientId: owners[0].Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: owners[0].Email, InboundId: inbound.Id, Up: 123, Down: 456}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&InboundService{}).UpdateInbound(tunnelOwnerRequest(t, owners[1].StableID, map[string]any{"id": inbound.Id})); err != nil {
		t.Fatal(err)
	}
	var traffic xray.ClientTraffic
	if err := db.Where("email = ?", owners[0].Email).First(&traffic).Error; err != nil || traffic.InboundId != 0 || traffic.Up != 123 || traffic.Down != 456 {
		t.Fatalf("canonical old owner history was not detached: %+v %v", traffic, err)
	}
}

func TestTunnelOwnerSelectionRejectsFilteredRemoteMirror(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owner := &model.ClientRecord{Email: "mirrored-default-owner", SubID: "mirrored-default-owner", Enable: true}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&InboundService{}).AddInbound(tunnelOwnerRequest(t, owner.StableID, nil)); err != nil {
		t.Fatal(err)
	}
	snapshot := &model.Inbound{Tag: "n1-mirrored-owner", Enable: true, Protocol: model.VLESS, Port: 24110, Settings: clientsSettings(t, []model.Client{*owner.ToClient()})}
	_, err := (&InboundService{}).setRemoteTrafficLocked(1, &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{snapshot}}, false, false)
	if !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("filtered default owner bypassed raw mirror guard: %v", err)
	}
	var count int64
	if err := db.Model(&model.Inbound{}).Where("node_id = ?", 1).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rejected mirror persisted remote settings: %d %v", count, err)
	}
}

func TestTunnelOwnerSelectionSerializesWithRemoteAttachment(t *testing.T) {
	for _, first := range []string{"local", "remote"} {
		t.Run(first, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			if db.Name() != "postgres" {
				t.Skip("requires PostgreSQL row locking")
			}
			owner := &model.ClientRecord{Email: "racing-tunnel-owner", SubID: "racing-owner", Enable: true}
			if err := db.Create(owner).Error; err != nil {
				t.Fatal(err)
			}
			remote := mkInbound(t, 24111, model.VLESS, `{"clients":[]}`)
			if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
				t.Fatal(err)
			}
			locked, release, attempted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var paused, observe, attemptedOnce atomic.Bool
			const callback = "test:tunnel-owner-race"
			if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "client_inbounds" && paused.CompareAndSwap(false, true) {
					close(locked)
					<-release
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if observe.Load() && tx.Statement.Table == "clients" && tx.Statement.Clauses["FOR"].Expression != nil && attemptedOnce.CompareAndSwap(false, true) {
					close(attempted)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = db.Callback().Create().Remove(callback)
				_ = db.Callback().Query().Remove(callback)
			})
			localWrite := func() error {
				_, _, err := (&InboundService{}).AddInbound(tunnelOwnerRequest(t, owner.StableID, nil))
				return err
			}
			remoteWrite := func() error {
				return db.Transaction(func(tx *gorm.DB) error {
					return (&ClientService{}).SyncInbound(tx, remote.Id, []model.Client{*owner.ToClient()})
				})
			}
			winner, loser := localWrite, remoteWrite
			if first == "remote" {
				winner, loser = remoteWrite, localWrite
			}
			won, lost := make(chan error, 1), make(chan error, 1)
			go func() { won <- winner() }()
			select {
			case <-locked:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("first writer did not reach the locked ownership write")
			}
			observe.Store(true)
			go func() { lost <- loser() }()
			select {
			case <-attempted:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("second writer did not attempt the shared identity lock")
			}
			close(release)
			if err := <-won; err != nil {
				t.Fatalf("first %s writer: %v", first, err)
			}
			if err := <-lost; !errors.Is(err, ErrClientPolicyLedger) {
				t.Fatalf("second writer crossed the scope boundary: %v", err)
			}
			var links int64
			if err := db.Model(&model.ClientInbound{}).Where("client_id = ?", owner.Id).Count(&links).Error; err != nil || links != 1 {
				t.Fatalf("racing assignments persisted %d memberships: %v", links, err)
			}
		})
	}
}

func TestTunnelOwnerSelectionUsesCanonicalClient(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owner := &model.ClientRecord{
		Email: "canonical-tunnel-owner", SubID: "owner-sub", UUID: uuid.NewString(),
		Flow: "xtls-rprx-vision", TotalGB: 9007199254740993, ExpiryTime: 1950000000000,
		Comment: "keep account", Policy: &model.ClientPolicyOptions{UploadBytesPerSecond: 12345, DownloadBytesPerSecond: 67890, Multiplier: "1.234567"},
	}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(owner).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(owner, owner.Id).Error; err != nil {
		t.Fatal(err)
	}
	before := *owner
	if err := db.Create(&xray.ClientTraffic{Email: owner.Email, Up: 123, Down: 456}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &InboundService{}
	request := tunnelOwnerRequest(t, owner.StableID, map[string]any{
		"disableFlow": true,
		"settings":    json.RawMessage(`{"address":"127.0.0.1","port":9001,"network":"tcp,udp","clients":[{"email":"injected-owner","enable":true,"totalGB":1}]}`),
	})
	inbound, _, err := svc.AddInbound(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"create", "update"} {
		if phase == "update" {
			request = tunnelOwnerRequest(t, owner.StableID, map[string]any{"id": inbound.Id, "disableFlow": true})
			inbound, _, err = svc.UpdateInbound(request)
			if err != nil {
				t.Fatal(err)
			}
		}
		links := linksOf(t, inbound.Id)
		if len(links) != 1 || links[owner.Id].ClientId != owner.Id {
			t.Fatalf("%s did not select the stable owner: %+v", phase, links)
		}
		var after model.ClientRecord
		if err := db.First(&after, owner.Id).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("%s overwrote canonical client fields", phase)
		}
		clients, err := svc.GetClients(inbound)
		if err != nil || len(clients) != 1 || clients[0].Email != owner.Email || clients[0].TotalGB != 9007199254740993 || clients[0].Flow != "xtls-rprx-vision" || clients[0].Enable {
			t.Fatalf("%s persisted a lossy owner snapshot: %+v %v", phase, clients, err)
		}
		var traffic xray.ClientTraffic
		if err := db.Where("email = ?", owner.Email).First(&traffic).Error; err != nil || traffic.Up != 123 || traffic.Down != 456 {
			t.Fatalf("%s changed prior owner usage: %+v %v", phase, traffic, err)
		}
		var count int64
		if err := db.Model(&model.ClientRecord{}).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%s created an unintended client: %d %v", phase, count, err)
		}
	}
}

func TestTunnelOwnerSelectionRejectsInvalidScopeAtomically(t *testing.T) {
	for _, scenario := range []string{"empty", "malformed", "unknown", "wrong-protocol", "remote-target", "remote-member", "client-stats"} {
		t.Run(scenario, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			owner := &model.ClientRecord{Email: "selected-owner", Enable: true}
			if err := db.Create(owner).Error; err != nil {
				t.Fatal(err)
			}
			id := owner.StableID
			fields := make(map[string]any)
			switch scenario {
			case "empty":
				id = ""
			case "malformed":
				id = "bad-identity"
			case "unknown":
				id = uuid.NewString()
			case "wrong-protocol":
				fields["protocol"] = "vless"
			case "remote-target":
				fields["nodeId"] = 7
			case "remote-member":
				remote := mkInbound(t, 24102, model.VLESS, `{"clients":[]}`)
				if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: remote.Id}).Error; err != nil {
					t.Fatal(err)
				}
			case "client-stats":
				fields["clientStats"] = []xray.ClientTraffic{{Email: owner.Email, Up: 99999}}
			}
			if _, _, err := (&InboundService{}).AddInbound(tunnelOwnerRequest(t, id, fields)); err == nil {
				t.Fatal("invalid owner command was accepted")
			}
			var count int64
			if err := db.Model(&model.Inbound{}).Where("port = ?", 24101).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("rejected command persisted an inbound: %d %v", count, err)
			}
			if err := db.Model(&xray.ClientTraffic{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("rejected command persisted traffic: %d %v", count, err)
			}
		})
	}
}

func TestTunnelOwnerReplacementPreservesHistoryThroughMigration(t *testing.T) {
	for _, sibling := range []bool{false, true} {
		t.Run(map[bool]string{false: "unattached", true: "sibling"}[sibling], func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			svc := &InboundService{}
			first := model.Client{Email: "former-owner", Enable: true, SubID: "first-owner"}
			inbound, _, err := svc.AddInbound(&model.Inbound{Protocol: model.Tunnel, Port: 24103, Settings: clientsSettings(t, []model.Client{first}), StreamSettings: `{}`})
			if err != nil {
				t.Fatal(err)
			}
			oldOwner := lookupClientRecord(t, first.Email)
			var siblingID int
			if sibling {
				other := mkInbound(t, 24104, model.Tunnel, clientsSettings(t, []model.Client{first}))
				if err := (&ClientService{}).SyncInbound(nil, other.Id, []model.Client{first}); err != nil {
					t.Fatal(err)
				}
				siblingID = other.Id
			}
			if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", first.Email).Updates(map[string]any{"up": 123, "down": 456}).Error; err != nil {
				t.Fatal(err)
			}
			replacement := *inbound
			replacement.Settings = clientsSettings(t, []model.Client{{Email: "new-owner", Enable: true, SubID: "new-owner"}})
			if _, _, err := svc.UpdateInbound(&replacement); err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"replacement", "restore-migration", "delete-listener"} {
				switch phase {
				case "restore-migration":
					if err := svc.MigrationRequirements(); err != nil {
						t.Fatal(err)
					}
					svc.MigrationRemoveOrphanedTraffics()
				case "delete-listener":
					if _, err := svc.DelInbound(inbound.Id); err != nil {
						t.Fatal(err)
					}
				}
				var traffic xray.ClientTraffic
				if err := db.Where("email = ?", first.Email).First(&traffic).Error; err != nil || traffic.Up != 123 || traffic.Down != 456 || traffic.InboundId != siblingID {
					t.Fatalf("%s lost/reassigned former owner history: %+v %v", phase, traffic, err)
				}
				if got := lookupClientRecord(t, first.Email); got.StableID != oldOwner.StableID {
					t.Fatalf("%s changed former owner identity", phase)
				}
				if phase != "delete-listener" {
					detail, err := svc.GetInboundDetail(inbound.Id)
					if err != nil {
						t.Fatal(err)
					}
					for _, stat := range detail.ClientStats {
						if stat.Email == first.Email {
							t.Fatalf("%s shows former owner under reassigned listener", phase)
						}
					}
				}
			}
		})
	}
}
